//go:build linux

package git

// Exercises exact-binary Git supervision, host-state inheritance, lifetime
// cancellation, and adopted descendant cleanup.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	gitFixtureModeEnvironment        = "TOBY_GIT_TEST_FIXTURE_MODE"
	gitFixtureOwnerEnvironment       = "TOBY_GIT_TEST_OWNER"
	gitFixtureExitEnvironment        = "TOBY_GIT_TEST_EXIT_CODE"
	gitFixtureValueEnvironment       = "TOBY_GIT_TEST_VALUE"
	gitFixtureExpectedSIDEnvironment = "TOBY_GIT_TEST_EXPECTED_SID"
	gitFixtureChildPIDEnvironment    = "TOBY_GIT_TEST_CHILD_PID_FILE"
	gitFixtureGitPIDEnvironment      = "TOBY_GIT_TEST_GIT_PID_FILE"
	gitFixtureSupervisorEnvironment  = "TOBY_GIT_TEST_SUPERVISOR_PID_FILE"
	gitFixtureReadyEnvironment       = "TOBY_GIT_TEST_READY_FILE"
	gitFixtureRepositoryEnvironment  = "TOBY_GIT_TEST_REPOSITORY"
)

const (
	gitFixtureNormal       = "normal"
	gitFixtureSameGroup    = "same_group"
	gitFixtureNewSession   = "new_session"
	gitFixtureDoubleFork   = "double_fork"
	gitFixtureIntermediate = "intermediate"
	gitFixtureLeaf         = "leaf"
	gitFixtureBlocking     = "blocking"
)

func TestMain(m *testing.M) {
	if code, handled := DispatchSupervisor(os.Args); handled {
		os.Exit(code)
	}
	if os.Getenv(gitFixtureOwnerEnvironment) == "1" {
		_ = os.Unsetenv(gitFixtureOwnerEnvironment)
		os.Exit(runGitFixtureOwner())
	}
	if mode := os.Getenv(gitFixtureModeEnvironment); mode != "" {
		os.Exit(runGitFixture(mode))
	}

	os.Exit(m.Run())
}

func TestSupervisedGitPreservesHostInputsAndExitCode(t *testing.T) {
	installGitFixture(t, gitFixtureNormal)
	t.Setenv(gitFixtureValueEnvironment, "host-environment-value")
	t.Setenv(gitFixtureExitEnvironment, "23")

	sessionID, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(
		gitFixtureExpectedSIDEnvironment,
		strconv.Itoa(sessionID),
	)

	repository := t.TempDir()
	repositoryDirectory, err := os.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	defer repositoryDirectory.Close()

	stdin := createGitFixtureFile(t, "stdin", "host-standard-input")
	stdout := createGitFixtureFile(t, "stdout", "")
	stderr := createGitFixtureFile(t, "stderr", "")

	code, runErr := newCommandRunner(nil).RunHostCommand(
		t.Context(),
		repositoryDirectory,
		[]string{"git", "fixture-operation"},
		stdin,
		stdout,
		stderr,
	)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if code != 23 {
		t.Fatalf("Git exit code = %d, want 23", code)
	}

	output := readGitFixtureCapture(t, stdout)
	for _, want := range []string{
		"environment=host-environment-value",
		"stdin=host-standard-input",
		"session=" + strconv.Itoa(sessionID),
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Git stdout = %q, want %q", output, want)
		}
	}
	if got := readGitFixtureCapture(t, stderr); got != "fixture-stderr\n" {
		t.Fatalf("Git stderr = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repository, "descriptor-marker")); err != nil {
		t.Fatalf("repository descriptor was not usable by Git: %v", err)
	}
}

func TestSupervisedGitReapsOutputHoldingDescendants(t *testing.T) {
	for _, mode := range []string{
		gitFixtureSameGroup,
		gitFixtureNewSession,
		gitFixtureDoubleFork,
	} {
		t.Run(mode, func(t *testing.T) {
			installGitFixture(t, mode)
			t.Setenv(gitFixtureExitEnvironment, "7")

			childPath := filepath.Join(t.TempDir(), "child.pid")
			readyPath := filepath.Join(t.TempDir(), "ready")
			supervisorPath := filepath.Join(t.TempDir(), "supervisor.pid")
			t.Setenv(gitFixtureChildPIDEnvironment, childPath)
			t.Setenv(gitFixtureReadyEnvironment, readyPath)
			t.Setenv(
				gitFixtureSupervisorEnvironment,
				supervisorPath,
			)

			repositoryDirectory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer repositoryDirectory.Close()
			stdin := createGitFixtureFile(t, "stdin", "")
			stdout := createGitFixtureFile(t, "stdout", "")
			stderr := createGitFixtureFile(t, "stderr", "")

			code, runErr := newCommandRunner(nil).RunHostCommand(
				t.Context(),
				repositoryDirectory,
				[]string{"git", "fixture-operation"},
				stdin,
				stdout,
				stderr,
			)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if code != 7 {
				t.Fatalf("Git exit code = %d, want 7", code)
			}

			childPID := readGitFixturePID(t, childPath)
			waitGitFixtureProcessStopped(t, childPID)
			if output := readGitFixtureCapture(t, stdout); !strings.Contains(
				output,
				"descendant-ready",
			) {
				t.Fatalf("Git stdout = %q", output)
			}
		})
	}
}

func TestSupervisedGitCancellationReapsDetachedDescendant(t *testing.T) {
	installGitFixture(t, gitFixtureBlocking)
	childPath := filepath.Join(t.TempDir(), "child.pid")
	gitPath := filepath.Join(t.TempDir(), "git.pid")
	readyPath := filepath.Join(t.TempDir(), "ready")
	supervisorPath := filepath.Join(t.TempDir(), "supervisor.pid")
	t.Setenv(gitFixtureChildPIDEnvironment, childPath)
	t.Setenv(gitFixtureGitPIDEnvironment, gitPath)
	t.Setenv(gitFixtureReadyEnvironment, readyPath)
	t.Setenv(gitFixtureSupervisorEnvironment, supervisorPath)

	repositoryDirectory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer repositoryDirectory.Close()
	stdin := createGitFixtureFile(t, "stdin", "")
	stdout := createGitFixtureFile(t, "stdout", "")
	stderr := createGitFixtureFile(t, "stderr", "")

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan gitFixtureRunResult, 1)
	go func() {
		code, err := newCommandRunner(nil).RunHostCommand(
			ctx,
			repositoryDirectory,
			[]string{"git", "fixture-operation"},
			stdin,
			stdout,
			stderr,
		)
		result <- gitFixtureRunResult{code: code, err: err}
	}()

	childPID := readGitFixturePID(t, childPath)
	gitPID := readGitFixturePID(t, gitPath)
	waitGitFixturePath(t, readyPath)
	cancel()

	select {
	case got := <-result:
		if got.code != 130 {
			t.Fatalf("canceled Git exit code = %d, want 130", got.code)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled Git error = %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Git supervisor did not finish exact cleanup")
	}

	waitGitFixtureProcessStopped(t, gitPID)
	waitGitFixtureProcessStopped(t, childPID)
}

func TestSupervisedGitOwnerDeathClosesLifetimeAndReapsTree(t *testing.T) {
	installGitFixture(t, gitFixtureBlocking)

	root := t.TempDir()
	childPath := filepath.Join(root, "child.pid")
	gitPath := filepath.Join(root, "git.pid")
	readyPath := filepath.Join(root, "ready")
	supervisorPath := filepath.Join(root, "supervisor.pid")
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitFixtureChildPIDEnvironment, childPath)
	t.Setenv(gitFixtureGitPIDEnvironment, gitPath)
	t.Setenv(gitFixtureReadyEnvironment, readyPath)
	t.Setenv(gitFixtureSupervisorEnvironment, supervisorPath)
	t.Setenv(gitFixtureRepositoryEnvironment, repository)

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(executable)
	owner.Env = append(os.Environ(), gitFixtureOwnerEnvironment+"=1")
	owner.Stdin = nil
	owner.Stdout = os.Stdout
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		_ = owner.Wait()
	})

	childPID := readGitFixturePID(t, childPath)
	gitPID := readGitFixturePID(t, gitPath)
	supervisorPID := readGitFixturePID(t, supervisorPath)
	waitGitFixturePath(t, readyPath)

	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Wait(); err == nil {
		t.Fatal("killed Git owner exited successfully")
	}

	waitGitFixtureProcessStopped(t, gitPID)
	waitGitFixtureProcessStopped(t, childPID)
	waitGitFixtureProcessNotRunning(t, supervisorPID)
}

func TestGitSupervisorArgumentsAreSealedAndBounded(t *testing.T) {
	arguments, err := createGitSupervisorArguments(
		[]string{"git", "commit", "-m", "private message"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer arguments.Close()

	if _, err := arguments.WriteAt([]byte("x"), 0); !errors.Is(
		err,
		unix.EPERM,
	) {
		t.Fatalf("sealed argument write error = %v, want EPERM", err)
	}
	decoded, err := readGitSupervisorArguments(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(decoded.Command, "\x00"); got !=
		"git\x00commit\x00-m\x00private message" {
		t.Fatalf("decoded Git command = %q", got)
	}
}

func TestGitCaptureIsAnonymousAndNonExecutable(t *testing.T) {
	capture, err := newGitCapture("stdout", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()

	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", capture.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "memfd:toby-git-stdout") {
		t.Fatalf("Git capture target = %q, want anonymous memfd", target)
	}
	info, err := capture.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("Git capture mode = %v, want non-executable", info.Mode())
	}
}

func TestGitProcessIdentitySignalsPidfdTarget(t *testing.T) {
	target := exec.Command("/bin/sleep", "30")
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = target.Process.Kill()
		_ = target.Wait()
	})
	replacement := exec.Command("/bin/sleep", "30")
	if err := replacement.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = replacement.Process.Kill()
		_ = replacement.Wait()
	})

	identity, err := openGitProcessIdentity(
		target.Process.Pid,
		os.Getpid(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()

	identity.pid = replacement.Process.Pid
	if err := identity.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := target.Wait(); err == nil {
		t.Fatal("pidfd-signaled Git target exited successfully")
	}
	if err := unix.Kill(replacement.Process.Pid, 0); err != nil {
		t.Fatalf("replacement process was signaled through numeric PID: %v", err)
	}
}

type gitFixtureRunResult struct {
	code int
	err  error
}

func runGitFixture(mode string) int {
	switch mode {
	case gitFixtureNormal:
		return runNormalGitFixture()
	case gitFixtureSameGroup:
		return runDescendantGitFixture(false)
	case gitFixtureNewSession:
		return runDescendantGitFixture(true)
	case gitFixtureDoubleFork:
		command, err := startGitFixtureChild(
			gitFixtureIntermediate,
			false,
		)
		if err != nil {
			return 91
		}
		if err := command.Wait(); err != nil {
			return 92
		}
		return gitFixtureExitCode()
	case gitFixtureIntermediate:
		command, err := startGitFixtureChild(gitFixtureLeaf, false)
		if err != nil {
			return 93
		}
		if err := writeGitFixturePID(
			os.Getenv(gitFixtureChildPIDEnvironment),
			command.Process.Pid,
		); err != nil {
			return 94
		}
		if err := waitGitFixtureReady(
			os.Getenv(gitFixtureReadyEnvironment),
		); err != nil {
			return 95
		}
		return 0
	case gitFixtureBlocking:
		if err := writeGitFixturePID(
			os.Getenv(gitFixtureGitPIDEnvironment),
			os.Getpid(),
		); err != nil {
			return 96
		}
		if err := writeGitFixturePID(
			os.Getenv(gitFixtureSupervisorEnvironment),
			os.Getppid(),
		); err != nil {
			return 97
		}
		command, err := startGitFixtureChild(gitFixtureLeaf, true)
		if err != nil {
			return 98
		}
		if err := writeGitFixturePID(
			os.Getenv(gitFixtureChildPIDEnvironment),
			command.Process.Pid,
		); err != nil {
			return 99
		}
		if err := waitGitFixtureReady(
			os.Getenv(gitFixtureReadyEnvironment),
		); err != nil {
			return 100
		}
		for {
			time.Sleep(time.Hour)
		}
	case gitFixtureLeaf:
		signalIgnoreGitFixture()
		if err := os.WriteFile(
			os.Getenv(gitFixtureReadyEnvironment),
			[]byte("ready"),
			0o600,
		); err != nil {
			return 101
		}
		fmt.Fprintln(os.Stdout, "descendant-ready")
		for {
			time.Sleep(time.Hour)
		}
	default:
		return 102
	}
}

func runNormalGitFixture() int {
	if len(os.Args) < 3 ||
		os.Args[1] != "-C" ||
		os.Args[2] != fmt.Sprintf(
			"/proc/self/fd/%d",
			gitSupervisorRepositoryFD,
		) {
		return 103
	}
	info, err := os.Stat(os.Args[2])
	if err != nil || !info.IsDir() {
		return 104
	}
	if err := os.WriteFile(
		filepath.Join(os.Args[2], "descriptor-marker"),
		[]byte("descriptor-authority"),
		0o600,
	); err != nil {
		return 105
	}

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 106
	}
	sessionID, err := unix.Getsid(0)
	if err != nil {
		return 107
	}
	expected, err := strconv.Atoi(
		os.Getenv(gitFixtureExpectedSIDEnvironment),
	)
	if err != nil || sessionID != expected {
		return 108
	}

	fmt.Fprintf(
		os.Stdout,
		"environment=%s\nstdin=%s\nsession=%d\n",
		os.Getenv(gitFixtureValueEnvironment),
		string(input),
		sessionID,
	)
	fmt.Fprintln(os.Stderr, "fixture-stderr")

	return gitFixtureExitCode()
}

func runDescendantGitFixture(newSession bool) int {
	if err := writeGitFixturePID(
		os.Getenv(gitFixtureSupervisorEnvironment),
		os.Getppid(),
	); err != nil {
		return 109
	}
	command, err := startGitFixtureChild(gitFixtureLeaf, newSession)
	if err != nil {
		return 110
	}
	if err := writeGitFixturePID(
		os.Getenv(gitFixtureChildPIDEnvironment),
		command.Process.Pid,
	); err != nil {
		return 111
	}
	if err := waitGitFixtureReady(
		os.Getenv(gitFixtureReadyEnvironment),
	); err != nil {
		return 112
	}

	return gitFixtureExitCode()
}

func runGitFixtureOwner() int {
	repository, err := os.Open(
		os.Getenv(gitFixtureRepositoryEnvironment),
	)
	if err != nil {
		return 113
	}
	defer repository.Close()

	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return 114
	}
	defer stdin.Close()
	stdout, err := newGitCapture("owner-stdout", nil)
	if err != nil {
		return 115
	}
	defer stdout.Close()
	stderr, err := newGitCapture("owner-stderr", nil)
	if err != nil {
		return 116
	}
	defer stderr.Close()

	code, err := newCommandRunner(nil).RunHostCommand(
		context.Background(),
		repository,
		[]string{"git", "fixture-operation"},
		stdin,
		stdout,
		stderr,
	)
	if err != nil {
		return 117
	}

	return code
}

func startGitFixtureChild(
	mode string,
	newSession bool,
) (*exec.Cmd, error) {
	command := exec.Command("/proc/self/exe")
	command.Env = replaceGitFixtureEnvironment(
		os.Environ(),
		gitFixtureModeEnvironment,
		mode,
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if newSession {
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := command.Start(); err != nil {
		return nil, err
	}

	return command, nil
}

func replaceGitFixtureEnvironment(
	environment []string,
	name string,
	value string,
) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}

	return append(result, prefix+value)
}

func signalIgnoreGitFixture() {
	// The supervisor must rely on SIGKILL and exact pidfds rather than a
	// cooperative descendant or a shared process group.
	signal.Ignore(
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	)
}

func gitFixtureExitCode() int {
	code, err := strconv.Atoi(os.Getenv(gitFixtureExitEnvironment))
	if err != nil || code < 0 || code > 255 {
		return 0
	}

	return code
}

func installGitFixture(t *testing.T, mode string) {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(gitFixtureModeEnvironment, mode)
}

func createGitFixtureFile(
	t *testing.T,
	name string,
	content string,
) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), name+"-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = file.Close()
	})
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	return file
}

func readGitFixtureCapture(t *testing.T, file *os.File) string {
	t.Helper()

	content, err := readGitCapture(file)
	if err != nil {
		t.Fatal(err)
	}

	return content
}

func writeGitFixturePID(path string, pid int) error {
	if path == "" {
		return fmt.Errorf("Git fixture PID path is empty")
	}

	temporary, err := os.CreateTemp(
		filepath.Dir(path),
		filepath.Base(path)+".tmp-*",
	)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if _, err := temporary.WriteString(strconv.Itoa(pid)); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	return os.Rename(temporaryPath, path)
}

func readGitFixturePID(t *testing.T, path string) int {
	t.Helper()

	waitGitFixturePath(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid Git fixture PID %q", strings.TrimSpace(string(data)))
	}

	return pid
}

func waitGitFixturePath(t *testing.T, path string) {
	t.Helper()

	if err := waitGitFixtureReady(path); err != nil {
		t.Fatal(err)
	}
}

func waitGitFixtureReady(path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Git fixture path %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitGitFixtureProcessStopped(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}

		if time.Now().After(deadline) {
			t.Fatalf("Git fixture process %d was not reaped", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitGitFixtureProcessNotRunning(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}

		fields := strings.Fields(string(status))
		if len(fields) >= 3 &&
			(fields[2] == "Z" || fields[2] == "X") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Git fixture process %d is still active", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
