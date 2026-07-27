//go:build linux

package bwrap

// Owns managed-terminal signal interception and child-process forwarding from
// before raw mode begins until the host terminal has been restored.

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type managedSignalForwarder struct {
	signals           <-chan os.Signal
	stopNotifications func()
	forward           func(os.Signal) error

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	closeErr error
}

func startManagedSignalForwarder(
	group *processGroupIdentity,
	externalInterrupts bool,
	registerSignalHandler func(func(syscall.Signal) error) func(),
) *managedSignalForwarder {
	signals := make(chan os.Signal, 4)
	localSignals := []os.Signal{syscall.SIGHUP, syscall.SIGQUIT}
	if !externalInterrupts {
		localSignals = append(
			localSignals,
			syscall.SIGINT,
			syscall.SIGTERM,
		)
	}
	signal.Notify(signals, localSignals...)
	unregister := registerProcessSignalHandler(
		registerSignalHandler,
		group,
	)

	return newManagedSignalForwarder(
		signals,
		func() {
			unregister()
			signal.Stop(signals)
		},
		func(current os.Signal) error {
			currentSignal, ok := current.(syscall.Signal)
			if !ok {
				return nil
			}
			return group.Signal(currentSignal)
		},
	)
}

func newManagedSignalForwarder(
	signals <-chan os.Signal,
	stopNotifications func(),
	forward func(os.Signal) error,
) *managedSignalForwarder {
	forwarder := &managedSignalForwarder{
		signals:           signals,
		stopNotifications: stopNotifications,
		forward:           forward,
		stop:              make(chan struct{}),
		done:              make(chan struct{}),
	}
	go forwarder.run()

	return forwarder
}

func (f *managedSignalForwarder) run() {
	defer close(f.done)
	for {
		select {
		case current := <-f.signals:
			f.forwardSignal(current)
		case <-f.stop:
			f.drain()
			return
		}
	}
}

func (f *managedSignalForwarder) drain() {
	for {
		select {
		case current := <-f.signals:
			f.forwardSignal(current)
		default:
			return
		}
	}
}

func (f *managedSignalForwarder) forwardSignal(current os.Signal) {
	if current == nil || f.forward == nil {
		return
	}
	err := f.forward(current)
	if err == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeErr = errors.Join(f.closeErr, err)
}

func (f *managedSignalForwarder) Close() error {
	if f == nil {
		return nil
	}

	f.closeOnce.Do(func() {
		if f.stopNotifications != nil {
			f.stopNotifications()
		}
		close(f.stop)
		<-f.done
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeErr
}
