//go:build linux

package bwrap

// Verifies the gated handoff from Bubblewrap status to exact foreground
// process and mount-namespace retention.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTrackForegroundStatusRetainsReportedMountNamespace(t *testing.T) {
	command, statusWriter, statusResult := startGatedForegroundStatusTest(t)
	namespace := foregroundTestMountNamespace(t, command.Process.Pid)

	if _, err := fmt.Fprintf(
		statusWriter,
		"{\"child-pid\":%d,\"mnt-namespace\":%d}\n",
		command.Process.Pid,
		namespace,
	); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(statusWriter, `{"exit-code":0}`); err != nil {
		t.Fatal(err)
	}
	if err := statusWriter.Close(); err != nil {
		t.Fatal(err)
	}

	tracked := <-statusResult
	if tracked.statusErr != nil || tracked.sandboxErr != nil {
		t.Fatalf(
			"foreground status errors = %v",
			[]error{tracked.statusErr, tracked.sandboxErr},
		)
	}
	if tracked.sandbox == nil {
		t.Fatal("foreground sandbox identity was not retained")
	}
	if err := finalizeForegroundSandbox(
		t.Context(),
		tracked.sandbox,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func TestTrackForegroundStatusReleasesGateAfterNamespaceMismatch(
	t *testing.T,
) {
	command, statusWriter, statusResult := startGatedForegroundStatusTest(t)
	namespace := foregroundTestMountNamespace(t, command.Process.Pid)

	if _, err := fmt.Fprintf(
		statusWriter,
		"{\"child-pid\":%d,\"mnt-namespace\":%d}\n",
		command.Process.Pid,
		namespace+1,
	); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(statusWriter, `{"exit-code":0}`); err != nil {
		t.Fatal(err)
	}
	if err := statusWriter.Close(); err != nil {
		t.Fatal(err)
	}

	tracked := <-statusResult
	if tracked.sandbox != nil {
		_ = tracked.sandbox.Close()
		t.Fatal("mismatched mount namespace was retained")
	}
	if tracked.sandboxErr == nil ||
		!strings.Contains(
			tracked.sandboxErr.Error(),
			"Bubblewrap reported",
		) {
		t.Fatalf("namespace mismatch error = %v", tracked.sandboxErr)
	}
}

func startGatedForegroundStatusTest(
	t *testing.T,
) (
	command *exec.Cmd,
	statusWriter *os.File,
	statusResult <-chan foregroundStatusResult,
) {
	t.Helper()

	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = statusReader.Close()
		_ = statusWriter.Close()
	})

	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = gateReader.Close()
	})

	command = exec.Command("sh", "-c", "IFS= read -r _ <&3 || :")
	command.ExtraFiles = []*os.File{gateReader}
	if err := command.Start(); err != nil {
		_ = gateWriter.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if err := gateReader.Close(); err != nil {
		_ = gateWriter.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}

	monitorStarted := make(chan int, 1)
	monitorStarted <- os.Getpid()
	close(monitorStarted)

	return command, statusWriter, trackForegroundStatus(
		statusReader,
		monitorStarted,
		gateWriter,
	)
}

func foregroundTestMountNamespace(t *testing.T, pid int) uint64 {
	t.Helper()

	var status unix.Stat_t
	if err := unix.Stat(
		fmt.Sprintf("/proc/%d/ns/mnt", pid),
		&status,
	); err != nil {
		t.Fatal(err)
	}

	return uint64(status.Ino)
}
