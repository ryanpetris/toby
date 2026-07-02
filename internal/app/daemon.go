// Daemon and daemon-client entry points. `toby daemon` runs the long-lived host
// process; `toby daemon status`/`stop` are thin control clients. Transport selection
// (unix socket vs WebSocket) is made here — the composition root — because a
// transport package cannot import its own implementations without a cycle.

package app

import (
	"context"
	"fmt"
	"os"

	"petris.dev/toby/config"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/client"
	"petris.dev/toby/internal/daemon"
	"petris.dev/toby/internal/daemon/configwatch"
	"petris.dev/toby/internal/daemon/transport/unixsocket"
	"petris.dev/toby/internal/daemon/transport/websocket"
	"petris.dev/toby/internal/tools/wiring"
	"petris.dev/toby/tools"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
)

// newDaemonCommand builds the `toby daemon` Cobra command: bare runs the long-lived
// server; the stop/ping/status subcommands are thin control clients.
func newDaemonCommand() *cobra.Command {
	var noIdleShutdown bool
	cmd := &cobra.Command{
		Use:           "daemon",
		Short:         "Run or control the Toby daemon.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return exitCode(runDaemon(daemon.Options{NoIdleShutdown: noIdleShutdown}))
		},
	}
	cmd.Flags().BoolVar(&noIdleShutdown, "no-idle-shutdown", false, "Keep the daemon running even when idle (for supervised/systemd mode).")
	cmd.AddCommand(
		&cobra.Command{
			Use: "stop", Short: "Ask a running daemon to shut down.",
			SilenceUsage: true, SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error { return exitCode(runDaemonStop()) },
		},
		&cobra.Command{
			Use: "ping", Short: "Ensure a daemon is running (spawning one if needed) and report it.",
			SilenceUsage: true, SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error { return exitCode(runDaemonPing()) },
		},
		&cobra.Command{
			Use: "status", Short: "Show daemon and project state.",
			SilenceUsage: true, SilenceErrors: true,
			RunE: func(*cobra.Command, []string) error { return exitCode(runDaemonStatus()) },
		},
	)
	return cmd
}

// newStopCommand builds the `toby stop [env]` command: with an env it stops that
// project's sandbox; with no env it stops the whole daemon.
func newStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "stop [env]",
		Short:         "Stop a project's sandbox, or the daemon when no env is given.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(_ *cobra.Command, args []string) error { return exitCode(runStopCommand(args)) },
	}
}

// exitCode maps an int exit code from the daemon control helpers to a Cobra error: 0
// is success; nonzero is a silent coded error, since the helpers already printed their
// own output and cli.ExecuteAndReport turns the coded error back into that exit code.
func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return exitcode.Code(code)
}

// transportModule selects the client<->daemon transport. Both ends must agree; the
// choice comes from TOBY_TRANSPORT (settings wiring folds in later).
func transportModule() fx.Option {
	if os.Getenv("TOBY_TRANSPORT") == "websocket" {
		return websocket.Module()
	}
	return unixsocket.Module()
}

// runDaemon runs the long-lived daemon process until a signal or daemon.stop.
func runDaemon(options daemon.Options) int {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(config.NewPaths),
		fx.Supply(options),
		wiring.PlanningModule(),
		tools.Module(),
		configwatch.Module(),
		transportModule(),
		daemon.Module(),
	)
	if err := app.Err(); err != nil {
		reportAppError(os.Stderr, err)
		return 1
	}

	startCtx, cancel := context.WithTimeout(context.Background(), app.StartTimeout())
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		reportAppError(os.Stderr, startErr)
		return 1
	}

	<-app.Wait()

	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	stopErr := app.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		reportAppError(os.Stderr, stopErr)
		return 1
	}
	return 0
}

// runDaemonPing ensures a daemon is running (spawning one if needed) and reports it.
func runDaemonPing() int {
	return withClient(func(svc *client.Service) int {
		result, err := svc.Ping(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("toby daemon %s (pid %d) ready\n", result.Version, result.PID)
		return 0
	})
}

// runDaemonStatus prints daemon and project state without spawning a daemon.
func runDaemonStatus() int {
	return withClient(func(svc *client.Service) int {
		status, err := svc.Status(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("toby daemon %s (pid %d), up %s\n", status.Version, status.PID, status.Uptime)
		if len(status.Projects) == 0 {
			fmt.Println("no active projects")
			return 0
		}
		for _, project := range status.Projects {
			fmt.Printf("  %s  container=%s  sessions=%d\n", project.Label, project.ContainerID, project.Sessions)
		}
		return 0
	})
}

// runStopCommand handles `toby stop [env]`: with an env it stops that project's
// container; with no env it stops the whole daemon. Neither spawns a daemon.
func runStopCommand(args []string) int {
	if len(args) == 0 || args[0] == "" {
		return runDaemonStop()
	}
	label := args[0]
	return withClient(func(svc *client.Service) int {
		stopped, err := svc.StopProject(context.Background(), label)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if stopped == 0 {
			fmt.Printf("no running project %q\n", label)
			return 0
		}
		fmt.Printf("stopped project %q\n", label)
		return 0
	})
}

// runDaemonStop asks a running daemon to shut down.
func runDaemonStop() int {
	return withClient(func(svc *client.Service) int {
		if err := svc.Stop(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("toby daemon stopping")
		return 0
	})
}

// withClient builds a client over the selected transport and runs fn.
func withClient(fn func(*client.Service) int) int {
	var svc *client.Service
	app := fx.New(
		fx.NopLogger,
		fx.Provide(config.NewPaths),
		transportModule(),
		client.Module(),
		fx.Populate(&svc),
	)
	if err := app.Err(); err != nil {
		reportAppError(os.Stderr, err)
		return 1
	}
	return fn(svc)
}
