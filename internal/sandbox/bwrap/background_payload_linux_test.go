//go:build linux

package bwrap

// Verifies exact initial-payload retention below a reaper-shaped process.

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRetainBackgroundPayloadUsesExactDirectChild(t *testing.T) {
	initCommand := exec.Command(
		"/bin/sh",
		"-c",
		`/bin/sleep 30 & wait`,
	)
	if err := initCommand.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = initCommand.Process.Kill()
			_ = initCommand.Wait()
		}
	})

	init, err := openProcessIdentity(
		initCommand.Process.Pid,
		os.Getpid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer init.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	payload, err := retainBackgroundPayload(ctx, init)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()

	parent, err := processParentPID(payload.pid)
	if err != nil {
		t.Fatal(err)
	}
	if parent != initCommand.Process.Pid {
		t.Fatalf(
			"retained payload parent = %d, want init %d",
			parent,
			initCommand.Process.Pid,
		)
	}

	if err := payload.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := initCommand.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
}
