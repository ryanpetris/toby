//go:build linux

package client

// Exercises the real detached-command launcher against a private
// agent helper process rather than an in-process launcher double.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
)

const (
	commandLauncherHelperEnvironment = "TOBY_COMMAND_LAUNCHER_TEST_HELPER"
	commandLauncherSocketEnvironment = "TOBY_COMMAND_LAUNCHER_TEST_SOCKET"
	commandLauncherPIDEnvironment    = "TOBY_COMMAND_LAUNCHER_TEST_PID"
	commandLauncherVersion           = "command-launcher-test-version"
)

func TestMain(m *testing.M) {
	if os.Getenv(commandLauncherHelperEnvironment) == "1" {
		os.Exit(runCommandLauncherHelper())
	}

	os.Exit(m.Run())
}

func TestCommandLauncherAutostartsDetachedAgentService(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtime := t.TempDir()
	path := filepath.Join(runtime, "toby", "agent.sock")
	pidPath := filepath.Join(runtime, "agent.pid")
	launcher := &CommandLauncher{
		Executable: executable,
		Environment: append(
			os.Environ(),
			commandLauncherHelperEnvironment+"=1",
			commandLauncherSocketEnvironment+"="+path,
			commandLauncherPIDEnvironment+"="+pidPath,
		),
	}
	service, err := NewService(
		path,
		commandLauncherVersion,
		launcher,
		ServiceOptions{
			StartupTimeout: 5 * time.Second,
			RetryMinimum:   time.Millisecond,
			RetryMaximum:   25 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	session, err := service.Connect(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	pid := readCommandLauncherPID(t, pidPath)
	helperNeedsCleanup := true
	t.Cleanup(func() {
		if helperNeedsCleanup {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	})
	if pid == os.Getpid() {
		t.Fatal("agent helper was not launched as a separate process")
	}
	processGroup, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if processGroup != pid {
		t.Fatalf(
			"agent helper process group = %d, want detached group %d",
			processGroup,
			pid,
		)
	}

	waitForCommandLauncherSocketRemoval(t, path, pid)
	helperNeedsCleanup = false
}

func runCommandLauncherHelper() int {
	if len(os.Args) != 1 {
		fmt.Fprintf(
			os.Stderr,
			"unexpected agent helper arguments: %q\n",
			os.Args,
		)
		return 2
	}
	path := os.Getenv(commandLauncherSocketEnvironment)
	pidPath := os.Getenv(commandLauncherPIDEnvironment)
	if path == "" || pidPath == "" {
		fmt.Fprintln(os.Stderr, "agent helper paths are missing")
		return 2
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	election, err := socket.Elect(ctx, path, socket.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if election.Listener == nil {
		fmt.Fprintln(
			os.Stderr,
			"agent helper lost an unexpected election",
		)
		_ = election.Conn.Close()
		return 1
	}
	service, err := server.New(
		commandLauncherVersion,
		&clientTestCoordinator{},
		server.Options{},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = election.Listener.Close()
		return 1
	}
	if err := os.WriteFile(
		pidPath,
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = election.Listener.Close()
		return 1
	}

	if err := service.Serve(
		ctx,
		election.Listener,
		server.ServeOptions{},
	); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func readCommandLauncherPID(t *testing.T, path string) int {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("agent helper PID %q is invalid", data)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("agent helper did not publish its PID")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForCommandLauncherSocketRemoval(
	t *testing.T,
	path string,
	pid int,
) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		processErr := syscall.Kill(pid, 0)
		_, socketErr := os.Lstat(path)
		if errors.Is(processErr, syscall.ESRCH) &&
			errors.Is(socketErr, os.ErrNotExist) {
			return
		}
		if socketErr != nil && !errors.Is(socketErr, os.ErrNotExist) {
			t.Fatal(socketErr)
		}
		if time.Now().After(deadline) {
			status, _ := os.ReadFile(
				filepath.Join("/proc", strconv.Itoa(pid), "status"),
			)
			t.Fatalf(
				"detached agent did not remove its socket; process check: %v; status: %s",
				processErr,
				status,
			)
		}
		time.Sleep(time.Millisecond)
	}
}
