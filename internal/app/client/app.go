// Package client is the Toby launch and management CLI composition root.
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/executable"
	"petris.dev/toby/internal/hostaction/methods/git"

	"go.uber.org/dig"
	"go.uber.org/fx"
)

// Run assembles and executes the Toby application.
func Run() {
	if err := executable.CheckUnprivileged(); err != nil {
		reportAppError(os.Stderr, err)
		os.Exit(1)
	}

	if code, handled := git.DispatchSupervisor(os.Args); handled {
		os.Exit(code)
	}

	var result *cliResult
	app := fx.New(Module(), fx.Populate(&result))
	os.Exit(runApp(app, result, os.Stderr))
}

// cliResult carries the CLI's exit code from the runCLI goroutine back to runApp.
// runApp blocks on the channel so the process stays alive until the command and
// its deferred native-run cleanup have fully completed. The buffer of one keeps
// the goroutine from blocking on send if runApp has already bailed out on a
// start error and is no longer receiving.
type cliResult struct{ ch chan int }

func newCLIResult() *cliResult { return &cliResult{ch: make(chan int, 1)} }

func runApp(app *fx.App, result *cliResult, stderr io.Writer) int {
	if stderr == nil {
		stderr = os.Stderr
	}
	if err := app.Err(); err != nil {
		reportAppError(stderr, err)
		return 1
	}
	startCtx, cancel := context.WithTimeout(
		context.Background(),
		app.StartTimeout(),
	)
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		reportAppError(stderr, startErr)
		return 1
	}

	// Block until the command finishes, including native-run teardown on
	// SIGTERM. Fx is deliberately not given the signal (we never call app.Wait),
	// so it cannot race that teardown to os.Exit and orphan Bubblewrap
	// processes, overlays, or leases; runCLI owns SIGTERM and reports the code
	// here.
	code := <-result.ch

	stopCtx, cancel := context.WithTimeout(
		context.Background(),
		app.StopTimeout(),
	)
	stopErr := app.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		reportAppError(stderr, stopErr)
		return 1
	}
	return code
}

func reportAppError(stderr io.Writer, err error) {
	if cause := dig.RootCause(err); cause != nil {
		var digErr dig.Error
		if !errors.As(cause, &digErr) {
			err = cause
		}
	}
	_, writeErr := fmt.Fprintln(stderr, err)
	diagnostic.DiscardError(
		"Fx construction is unavailable",
		"write early application error",
		writeErr,
	)
}
