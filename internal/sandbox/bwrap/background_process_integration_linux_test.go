//go:build linux

package bwrap

// Proves background Bubblewrap parent-death behavior against both Go thread
// retirement and abrupt process death on a supported Linux host.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	backgroundDeathHelperEnvironment = "TOBY_BACKGROUND_DEATH_HELPER"
	backgroundDeathRootEnvironment   = "TOBY_BACKGROUND_DEATH_ROOT"
)

func TestBackgroundProcessStopRunsPayloadTermHandler(t *testing.T) {
	if os.Getenv("TOBY_BWRAP_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_INTEGRATION=1 on the target Linux host")
	}

	root := secureBackgroundIntegrationPath(t)
	executor := newBackgroundIntegrationExecutor(t, root)
	process, err := executor.StartBackground(
		t.Context(),
		backgroundTermIntegrationInvocation(root, false),
		ProcessIO{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill(context.Background())
		<-process.Done()
	})

	waitForBackgroundProcessFile(
		t,
		filepath.Join(root, "ready"),
		process,
	)
	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForBackgroundProcessDone(t, process)

	if _, err := os.Stat(filepath.Join(root, "term-handled")); err != nil {
		t.Fatalf("payload TERM handler did not run: %v", err)
	}
}

func TestBackgroundProcessEscalatesIgnoredTerm(t *testing.T) {
	if os.Getenv("TOBY_BWRAP_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_INTEGRATION=1 on the target Linux host")
	}

	root := secureBackgroundIntegrationPath(t)
	executor := newBackgroundIntegrationExecutor(t, root)
	process, err := executor.StartBackground(
		t.Context(),
		backgroundTermIntegrationInvocation(root, true),
		ProcessIO{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Kill(context.Background())
		<-process.Done()
	})

	waitForBackgroundProcessFile(
		t,
		filepath.Join(root, "ready"),
		process,
	)
	concrete := process.(*backgroundProcess)
	concrete.mu.Lock()
	monitorSource := concrete.monitor
	initSource := concrete.init
	payloadSource := concrete.payload
	concrete.mu.Unlock()
	monitor := duplicateBackgroundIdentity(t, monitorSource)
	init := duplicateBackgroundIdentity(t, initSource)
	payload := duplicateBackgroundIdentity(t, payloadSource)
	defer closeBackgroundIdentities(
		[]*processIdentity{monitor, init, payload},
	)

	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
		t.Fatalf(
			"TERM-ignoring payload exited without escalation: %v",
			process.Err(),
		)
	case <-time.After(250 * time.Millisecond):
	}
	exited, err := payload.Exited()
	if err != nil {
		t.Fatal(err)
	}
	if exited {
		t.Fatal("TERM-ignoring payload exited before forced termination")
	}

	if err := process.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForBackgroundProcessDone(t, process)
	waitForRetainedProcessExit(t, monitor)
	waitForRetainedProcessExit(t, init)
	waitForRetainedProcessExit(t, payload)
	if process.Err() == nil {
		t.Fatal("forced background termination did not retain an error")
	}
}

func TestBackgroundProcessSurvivesStartingThreadExit(t *testing.T) {
	if os.Getenv("TOBY_BWRAP_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_INTEGRATION=1 on the target Linux host")
	}

	root := secureBackgroundIntegrationPath(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	executor, err := NewExecutor(ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	type launchResult struct {
		process BackgroundProcess
		err     error
	}
	type launchThread struct {
		tid      int
		launch   chan bool
		result   chan launchResult
		returned chan struct{}
	}
	threads := make(chan *launchThread, 2)
	for range 2 {
		go func() {
			runtime.LockOSThread()
			thread := &launchThread{
				tid:      unix.Gettid(),
				launch:   make(chan bool),
				result:   make(chan launchResult, 1),
				returned: make(chan struct{}),
			}
			threads <- thread

			if !<-thread.launch {
				runtime.UnlockOSThread()
				close(thread.returned)
				return
			}

			process, err := executor.StartBackground(
				ctx,
				backgroundIntegrationInvocation(root),
				ProcessIO{},
				nil,
			)
			thread.result <- launchResult{
				process: process,
				err:     err,
			}
			close(thread.returned)
		}()
	}

	first := <-threads
	second := <-threads
	if first.tid == second.tid {
		t.Fatalf("locked launch workers share thread %d", first.tid)
	}
	selected := first
	other := second
	if selected.tid == os.Getpid() {
		selected, other = other, selected
	}
	if selected.tid == os.Getpid() {
		t.Fatal("both locked launch workers used the Linux main thread")
	}

	selected.launch <- true
	other.launch <- false
	result := <-selected.result
	<-selected.returned
	<-other.returned
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(func() {
		_ = result.process.Kill(context.Background())
		<-result.process.Done()
	})

	waitForBackgroundThreadExit(t, selected.tid)
	waitForBackgroundProcessFile(
		t,
		filepath.Join(root, "ready"),
		result.process,
	)

	select {
	case <-result.process.Done():
		t.Fatalf(
			"background Bubblewrap followed its retired Go caller thread: %v",
			result.process.Err(),
		)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestBackgroundProcessDiesWithOwningProcess(t *testing.T) {
	if os.Getenv(backgroundDeathHelperEnvironment) == "1" {
		runBackgroundDeathHelper(t)
		return
	}
	if os.Getenv("TOBY_BWRAP_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_INTEGRATION=1 on the target Linux host")
	}

	root := secureBackgroundIntegrationPath(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var helperOutput bytes.Buffer
	helper := exec.Command(
		executable,
		"-test.run=^TestBackgroundProcessDiesWithOwningProcess$",
	)
	helper.Env = append(
		os.Environ(),
		backgroundDeathHelperEnvironment+"=1",
		backgroundDeathRootEnvironment+"="+root,
	)
	helper.Stdout = &helperOutput
	helper.Stderr = &helperOutput
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan error, 1)
	go func() {
		helperDone <- helper.Wait()
	}()
	helperWaited := false
	t.Cleanup(func() {
		if !helperWaited {
			_ = helper.Process.Kill()
			<-helperDone
		}
	})

	bubblewrapPID := waitForBackgroundIntegrationPID(
		t,
		filepath.Join(root, "bubblewrap.pid"),
		helperDone,
		&helperWaited,
		&helperOutput,
	)
	waitForBackgroundIntegrationFile(
		t,
		filepath.Join(root, "ready"),
		helperDone,
		&helperWaited,
		&helperOutput,
	)

	bubblewrap, err := openProcessIdentity(
		bubblewrapPID,
		helper.Process.Pid,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer bubblewrap.Close()

	descendants, err := retainBackgroundDescendants(bubblewrapPID)
	if err != nil {
		t.Fatal(err)
	}
	if len(descendants) < 2 {
		t.Fatalf(
			"Bubblewrap process tree has %d descendants, want at least init and payload",
			len(descendants),
		)
	}
	defer func() {
		for _, descendant := range descendants {
			_ = descendant.Close()
		}
	}()

	foreground := exec.Command("sleep", "30")
	if err := foreground.Start(); err != nil {
		t.Fatal(err)
	}
	foregroundIdentity, err := openProcessIdentity(
		foreground.Process.Pid,
		os.Getpid(),
	)
	if err != nil {
		_ = foreground.Process.Kill()
		_ = foreground.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = foregroundIdentity.Signal(syscall.SIGKILL)
		_ = foreground.Wait()
		_ = foregroundIdentity.Close()
	})

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-helperDone; err == nil {
		t.Fatal("killed background owner exited without a signal")
	}
	helperWaited = true

	waitForRetainedProcessExit(t, bubblewrap)
	for _, descendant := range descendants {
		waitForRetainedProcessExit(t, descendant)
	}

	exited, err := foregroundIdentity.Exited()
	if err != nil {
		t.Fatal(err)
	}
	if exited {
		t.Fatal(
			"abrupt agent-owner death terminated an independent foreground process",
		)
	}
}

func newBackgroundIntegrationExecutor(
	t *testing.T,
	root string,
) *Executor {
	t.Helper()

	executor, err := NewExecutor(ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	return executor
}

func backgroundTermIntegrationInvocation(
	root string,
	ignore bool,
) *Invocation {
	trap := `trap ': > "$term_marker"; exit 0' TERM`
	if ignore {
		trap = `trap '' TERM`
	}
	script := backgroundTerminationPrefixFixture + trap + backgroundTerminationSuffixFixture
	args := namespaceArgs(os.Getuid(), os.Getgid())
	args = append(
		args,
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--bind", root, root,
		"--",
		"/bin/sh", "-c", script, "toby-background-term",
		filepath.Join(root, "term-handled"),
		filepath.Join(root, "ready"),
		"/bin/sleep",
	)

	return &Invocation{
		Args: args,
		Mode: ExecutionNonInteractive,
	}
}

func waitForBackgroundProcessDone(
	t *testing.T,
	process BackgroundProcess,
) {
	t.Helper()

	select {
	case <-process.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("background Bubblewrap was not reaped")
	}
}

func duplicateBackgroundIdentity(
	t *testing.T,
	identity *processIdentity,
) *processIdentity {
	t.Helper()

	descriptor, err := unix.Dup(identity.pidfd)
	if err != nil {
		t.Fatal(err)
	}

	return &processIdentity{
		pid:   identity.pid,
		pidfd: descriptor,
	}
}

func waitForBackgroundThreadExit(t *testing.T, thread int) {
	t.Helper()

	path := filepath.Join(
		"/proc/self/task",
		strconv.Itoa(thread),
	)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return
		} else if err != nil {
			t.Fatal(err)
		}

		select {
		case <-timer.C:
			t.Fatalf("launching Go thread %d did not terminate", thread)
		case <-ticker.C:
		}
	}
}

func waitForBackgroundProcessFile(
	t *testing.T,
	path string,
	process BackgroundProcess,
) {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		select {
		case <-process.Done():
			t.Fatalf(
				"background Bubblewrap exited before publishing %s: %v",
				filepath.Base(path),
				process.Err(),
			)
		case <-timer.C:
			t.Fatalf(
				"background Bubblewrap did not publish %s",
				filepath.Base(path),
			)
		case <-ticker.C:
		}
	}
}

func runBackgroundDeathHelper(t *testing.T) {
	root := os.Getenv(backgroundDeathRootEnvironment)
	if root == "" {
		t.Fatal("background death helper root is missing")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	executor, err := NewExecutor(ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()

	process, err := executor.StartBackground(
		ctx,
		backgroundIntegrationInvocation(root),
		ProcessIO{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	concrete := process.(*backgroundProcess)
	concrete.mu.Lock()
	bubblewrapPID := concrete.monitor.pid
	concrete.mu.Unlock()
	if err := os.WriteFile(
		filepath.Join(root, "bubblewrap.pid"),
		[]byte(strconv.Itoa(bubblewrapPID)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	select {
	case <-process.Done():
		t.Fatalf(
			"background Bubblewrap exited before its owner: %v",
			process.Err(),
		)
	case <-time.After(10 * time.Minute):
		t.Fatal("background death helper timed out")
	}
}

func backgroundIntegrationInvocation(root string) *Invocation {
	script := backgroundDescendantScriptFixture
	args := namespaceArgs(os.Getuid(), os.Getgid())
	args = append(
		args,
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--bind", root, root,
		"--",
		"/bin/sh", "-c", script, "toby-background-owner",
		"/bin/sh", "/bin/sleep",
		filepath.Join(root, "child-ready"),
		filepath.Join(root, "ready"),
	)

	return &Invocation{
		Args: args,
		Mode: ExecutionNonInteractive,
	}
}

func secureBackgroundIntegrationPath(t *testing.T) string {
	t.Helper()

	path, err := os.MkdirTemp(".", ".toby-background-process-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})

	return absolute
}

func waitForBackgroundIntegrationPID(
	t *testing.T,
	path string,
	helperDone <-chan error,
	helperWaited *bool,
	output *bytes.Buffer,
) int {
	t.Helper()

	waitForBackgroundIntegrationFile(
		t,
		path,
		helperDone,
		helperWaited,
		output,
	)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid background integration PID %q", data)
	}

	return pid
}

func waitForBackgroundIntegrationFile(
	t *testing.T,
	path string,
	helperDone <-chan error,
	helperWaited *bool,
	output *bytes.Buffer,
) {
	t.Helper()

	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-helperDone:
			*helperWaited = true
			t.Fatalf(
				"background helper exited before publishing %s: %v; output=%s",
				filepath.Base(path),
				err,
				output.String(),
			)
		case <-timer.C:
			t.Fatalf(
				"background helper did not publish %s; output=%s",
				filepath.Base(path),
				output.String(),
			)
		case <-ticker.C:
		}
	}
}

func waitForRetainedProcessExit(
	t *testing.T,
	process *processIdentity,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := waitForProcessExit(ctx, process); err != nil {
		t.Fatal(fmt.Errorf(
			"retained process %d survived owner death: %w",
			process.pid,
			err,
		))
	}
}

func waitForProcessExit(
	ctx context.Context,
	process *processIdentity,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		exited, err := process.Exited()
		if err != nil {
			return err
		}
		if exited {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func retainBackgroundDescendants(
	rootPID int,
) ([]*processIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	children := make(map[int][]int)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		parentPID, err := processParentPID(pid)
		if err != nil {
			continue
		}
		children[parentPID] = append(children[parentPID], pid)
	}

	type processEdge struct {
		pid    int
		parent int
	}
	var retained []*processIdentity
	pending := make([]processEdge, 0, len(children[rootPID]))
	for _, pid := range children[rootPID] {
		pending = append(pending, processEdge{
			pid:    pid,
			parent: rootPID,
		})
	}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]

		identity, err := openProcessIdentity(
			current.pid,
			current.parent,
		)
		if err != nil {
			closeBackgroundIdentities(retained)
			return nil, err
		}
		retained = append(retained, identity)
		for _, pid := range children[current.pid] {
			pending = append(pending, processEdge{
				pid:    pid,
				parent: current.pid,
			})
		}
	}

	return retained, nil
}

func closeBackgroundIdentities(identities []*processIdentity) {
	for _, identity := range identities {
		_ = identity.Close()
	}
}
