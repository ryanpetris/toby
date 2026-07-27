//go:build linux

package bwrap

// Verifies managed-terminal signal forwarding owns pending notifications and
// joins its goroutine before returning cleanup errors.

import (
	"errors"
	"os"
	"slices"
	"syscall"
	"testing"
)

func TestManagedSignalForwarderDrainsPendingSignalsOnClose(t *testing.T) {
	signals := make(chan os.Signal, 2)
	var forwarded []os.Signal
	sentinel := errors.New("forward signal")
	notificationsStopped := false

	forwarder := newManagedSignalForwarder(
		signals,
		func() {
			notificationsStopped = true
			signals <- syscall.SIGHUP
		},
		func(current os.Signal) error {
			forwarded = append(forwarded, current)
			if current == syscall.SIGHUP {
				return sentinel
			}
			return nil
		},
	)
	signals <- syscall.SIGTERM

	if err := forwarder.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("forwarder cleanup error = %v, want %v", err, sentinel)
	}
	if !notificationsStopped {
		t.Fatal("signal notifications were not stopped")
	}
	if !slices.Equal(
		forwarded,
		[]os.Signal{syscall.SIGTERM, syscall.SIGHUP},
	) {
		t.Fatalf("forwarded signals = %v", forwarded)
	}
	if err := forwarder.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("second forwarder cleanup error = %v, want %v", err, sentinel)
	}
}
