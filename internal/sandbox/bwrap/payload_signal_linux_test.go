//go:build linux

package bwrap

// Verifies queued delivery through exact payload process descriptors.

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPayloadSignalTargetDeliversSignalQueuedBeforeAttach(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(
		"/bin/sh",
		"-c",
		`trap 'exit 0' INT; touch "$1"; while :; do sleep 0.05; done`,
		"payload",
		ready,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := command.Process.Kill(); err != nil {
				t.Error(err)
			}
			if err := command.Wait(); err != nil {
				t.Error(err)
			}
		}
	})
	waitForTestFile(t, ready)

	target := newPayloadSignalTarget()
	defer target.Close()
	if err := target.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(command.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Attach(pidfd); err != nil {
		t.Fatal(err)
	}

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued payload signal was not delivered")
	}
}
