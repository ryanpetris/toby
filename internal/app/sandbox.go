// In-container `toby sandbox <role>` dispatch. These run inside sandbox containers
// (gated by TOBY_SANDBOX=1) and need no heavy fx graph — they are dispatched early,
// before the launch CLI is built, and construct their tiny runners directly. Roles:
// idle (keep-alive main), home (files+exec manager), netns (proxy manager), launch
// (the tool container's main — exec the tool from a descriptor).

package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"petris.dev/toby/internal/control/sandbox"
)

func runSandboxCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox: a role is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "idle":
		<-ctx.Done()
		return 0
	case "home":
		return reportSandboxErr(sandbox.NewHomeRunner().Run(ctx))
	case "netns":
		return reportSandboxErr(sandbox.NewNetnsRunner().Run(ctx))
	case "launch":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "sandbox launch: descriptor path required")
			return 2
		}
		return reportSandboxErr(sandbox.NewLaunchRunner().Run(args[1]))
	default:
		fmt.Fprintf(os.Stderr, "sandbox: unknown role %q\n", args[0])
		return 2
	}
}

func reportSandboxErr(err error) int {
	if err == nil || err == context.Canceled {
		return 0
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
