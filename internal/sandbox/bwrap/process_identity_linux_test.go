//go:build linux

package bwrap

// Verifies that parent-death cleanup targets retained pidfds rather than
// mutable numeric PID slots.

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProcessIdentitySignalsRetainedTarget(t *testing.T) {
	target := startProcessIdentityTestProcess(t)
	replacement := startProcessIdentityTestProcess(t)

	identity, err := openProcessIdentity(target.Process.Pid, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()

	identity.pid = replacement.Process.Pid
	if err := identity.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForProcessIdentityTestExit(t, target)

	if err := unix.Kill(replacement.Process.Pid, 0); err != nil {
		t.Fatalf("numeric-PID process was signaled instead of pidfd target: %v", err)
	}
}

func TestProcessIdentityRejectsUnexpectedParent(t *testing.T) {
	target := startProcessIdentityTestProcess(t)

	identity, err := openProcessIdentity(
		target.Process.Pid,
		target.Process.Pid,
	)
	if err == nil {
		identity.Close()
		t.Fatal("process with unexpected parent was retained")
	}
	if !strings.Contains(err.Error(), "parent is") {
		t.Fatalf("unexpected-parent error = %v", err)
	}
	if err := unix.Kill(target.Process.Pid, 0); err != nil {
		t.Fatalf("rejected process was signaled: %v", err)
	}
}

func TestExitedProcessIdentityDoesNotSignalReplacement(t *testing.T) {
	target := startProcessIdentityTestProcess(t)
	identity, err := openProcessIdentity(target.Process.Pid, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()

	if err := target.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitForProcessIdentityTestExit(t, target)

	replacement := startProcessIdentityTestProcess(t)
	identity.pid = replacement.Process.Pid
	if err := identity.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	if err := unix.Kill(replacement.Process.Pid, 0); err != nil {
		t.Fatalf("replacement process was signaled through stale identity: %v", err)
	}
	exited, err := identity.Exited()
	if err != nil {
		t.Fatal(err)
	}
	if !exited {
		t.Fatal("exited pidfd target was reported alive")
	}
}

func startProcessIdentityTestProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	return command
}

func waitForProcessIdentityTestExit(t *testing.T, command *exec.Cmd) {
	t.Helper()

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	select {
	case <-wait:
	case <-time.After(2 * time.Second):
		t.Fatalf("process %d did not exit", command.Process.Pid)
	}
}
