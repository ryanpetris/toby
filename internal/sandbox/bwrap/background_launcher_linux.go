//go:build linux

package bwrap

// Keeps the Bubblewrap monitor's Linux parent thread alive for the lifetime of
// background resources that rely on --die-with-parent.

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
)

type backgroundLauncher struct {
	mu sync.Mutex

	requests chan backgroundLaunchRequest
	done     chan struct{}
	closed   bool
}

type backgroundLaunchRequest struct {
	command *exec.Cmd
	result  chan error
}

func newBackgroundLauncher() *backgroundLauncher {
	launcher := &backgroundLauncher{
		requests: make(chan backgroundLaunchRequest),
		done:     make(chan struct{}),
	}
	ready := make(chan struct{})
	go launcher.run(ready)
	<-ready

	return launcher
}

func (l *backgroundLauncher) run(ready chan<- struct{}) {
	// Bubblewrap binds --die-with-parent to this exact Linux thread. Keep the
	// goroutine locked until Executor.Close, after all owned processes reap.
	runtime.LockOSThread()
	defer close(l.done)
	close(ready)

	for request := range l.requests {
		request.result <- request.command.Start()
	}
}

func (l *backgroundLauncher) Start(
	ctx context.Context,
	command *exec.Cmd,
) error {
	if l == nil {
		return fmt.Errorf("background Bubblewrap launcher is not configured")
	}
	if ctx == nil {
		return fmt.Errorf("start background Bubblewrap: context is nil")
	}
	if command == nil {
		return fmt.Errorf("background Bubblewrap command is nil")
	}

	request := backgroundLaunchRequest{
		command: command,
		result:  make(chan error, 1),
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return fmt.Errorf("background Bubblewrap launcher is closed")
	}
	select {
	case l.requests <- request:
		l.mu.Unlock()
	case <-ctx.Done():
		l.mu.Unlock()
		return ctx.Err()
	}

	return <-request.result
}

func (l *backgroundLauncher) Close() {
	if l == nil {
		return
	}

	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.requests)
	}
	done := l.done
	l.mu.Unlock()

	<-done
}
