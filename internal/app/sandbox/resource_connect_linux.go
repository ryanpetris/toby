//go:build linux

package sandbox

// Runs the exact sandbox-helper resource connector invocation before application
// configuration and process-wide dependency injection are constructed.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandboxgateway"
)

type resourceConnectorSignalSource func() (<-chan os.Signal, func())

type resourceConnectFunc func(
	context.Context,
	string,
	string,
	io.ReadCloser,
	io.Writer,
) error

// DispatchResourceConnector recognizes the generated sandbox resource command.
// Callers must invoke it before constructing Fx, config.Paths, or host
// application configuration.
func DispatchResourceConnector(
	arguments []string,
	stdin io.ReadCloser,
	stdout io.Writer,
	stderr io.Writer,
) (code int, handled bool) {
	return dispatchResourceConnector(
		arguments,
		stdin,
		stdout,
		stderr,
		sandboxgateway.Connect,
		resourceConnectorSignals,
	)
}

func dispatchResourceConnector(
	arguments []string,
	stdin io.ReadCloser,
	stdout io.Writer,
	stderr io.Writer,
	connect resourceConnectFunc,
	signals resourceConnectorSignalSource,
) (code int, handled bool) {
	resourceID, handled := resourceConnectorInvocation(arguments)
	if !handled {
		return 0, false
	}

	signalChannel, stopSignals := signals()
	defer stopSignals()

	interrupted, err := runResourceConnector(
		resourceID,
		stdin,
		stdout,
		connect,
		signalChannel,
	)
	if interrupted != nil {
		return resourceConnectorSignalExitCode(interrupted), true
	}
	if err != nil {
		_, writeErr := fmt.Fprintln(stderr, err)
		diagnostic.DiscardError(
			"Fx construction is unavailable",
			"write sandbox resource connector error",
			writeErr,
		)
		return 1, true
	}

	return 0, true
}

func resourceConnectorInvocation(arguments []string) (string, bool) {
	if len(arguments) != 5 ||
		arguments[1] != "resource" ||
		arguments[2] != "connect" ||
		arguments[3] != "--" {
		return "", false
	}

	resourceID := arguments[4]
	if resourceID == "" {
		return "", false
	}

	return resourceID, true
}

func resourceConnectorSignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 4)
	signal.Notify(
		signals,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	)

	return signals, func() {
		signal.Stop(signals)
	}
}

func runResourceConnector(
	resourceID string,
	stdin io.ReadCloser,
	stdout io.Writer,
	connect resourceConnectFunc,
	signals <-chan os.Signal,
) (os.Signal, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan os.Signal, 1)
	watcherDone := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()

		select {
		case current := <-signals:
			received <- current
			cancel()
		case <-watcherDone:
		}
	}()

	err := connect(
		ctx,
		layout.SandboxSocket(),
		resourceID,
		stdin,
		stdout,
	)
	close(watcherDone)
	watcher.Wait()

	select {
	case current := <-received:
		return current, err
	default:
		return nil, err
	}
}

func resourceConnectorSignalExitCode(current os.Signal) int {
	signal, ok := current.(syscall.Signal)
	if !ok {
		return 1
	}

	code := 128 + int(signal)
	if code < 0 || code > 255 {
		return 1
	}

	return code
}
