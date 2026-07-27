//go:build linux

package git

// Runs the private early-process Git supervisor before normal application
// composition, retaining host credentials and terminal state.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

// DispatchSupervisor recognizes and runs the private Git supervisor mode.
// Callers must invoke it before constructing Fx or Cobra state.
func DispatchSupervisor(arguments []string) (code int, handled bool) {
	if len(arguments) < 2 || arguments[1] != gitSupervisorArgument {
		return 0, false
	}
	if len(arguments) != 2 {
		return 1, true
	}

	return runGitSupervisor(), true
}

func runGitSupervisor() int {
	statusFile := os.NewFile(
		uintptr(gitSupervisorStatusFD),
		"toby-git-supervisor-status",
	)
	if statusFile == nil {
		return 1
	}
	unix.CloseOnExec(gitSupervisorStatusFD)

	status := superviseGitCommand()
	if err := writeGitSupervisorStatus(statusFile, status); err != nil {
		diagnostic.DiscardError(
			"the early Git supervisor has no diagnostic sink",
			"close Git supervisor status descriptor",
			statusFile.Close(),
		)
		return 1
	}
	diagnostic.DiscardError(
		"the foreground Git result is already determined",
		"close Git supervisor status descriptor",
		statusFile.Close(),
	)

	return status.ExitCode
}

func superviseGitCommand() gitSupervisorStatus {
	signals := make(chan os.Signal, 4)
	signal.Notify(
		signals,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	)
	defer signal.Stop(signals)

	repository := os.NewFile(
		uintptr(gitSupervisorRepositoryFD),
		"toby-git-repository",
	)
	lifetime := os.NewFile(
		uintptr(gitSupervisorLifetimeFD),
		"toby-git-supervisor-lifetime",
	)
	argumentsFile := os.NewFile(
		uintptr(gitSupervisorArgumentsFD),
		"toby-git-supervisor-arguments",
	)
	if repository == nil || lifetime == nil || argumentsFile == nil {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("git supervisor inherited an invalid descriptor"),
		)
	}
	repositoryOpen := true
	defer func() {
		if repositoryOpen {
			diagnostic.DiscardError(
				"the early Git supervisor has no diagnostic sink",
				"close Git repository descriptor",
				repository.Close(),
			)
		}
	}()
	defer func() {
		diagnostic.DiscardError(
			"the early Git supervisor has no diagnostic sink",
			"close Git supervisor lifetime descriptor",
			lifetime.Close(),
		)
	}()
	argumentsOpen := true
	defer func() {
		if argumentsOpen {
			diagnostic.DiscardError(
				"the early Git supervisor has no diagnostic sink",
				"close Git supervisor arguments descriptor",
				argumentsFile.Close(),
			)
		}
	}()
	unix.CloseOnExec(gitSupervisorRepositoryFD)
	unix.CloseOnExec(gitSupervisorLifetimeFD)
	unix.CloseOnExec(gitSupervisorArgumentsFD)

	repositoryInfo, err := repository.Stat()
	if err != nil {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("inspect Git repository descriptor: %w", err),
		)
	}
	if !repositoryInfo.IsDir() {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("git repository descriptor is not a directory"),
		)
	}

	lifetimeInfo, err := lifetime.Stat()
	if err != nil {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("inspect Git supervisor lifetime descriptor: %w", err),
		)
	}
	if lifetimeInfo.Mode()&os.ModeNamedPipe == 0 {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("git supervisor lifetime descriptor is not a pipe"),
		)
	}

	arguments, err := readGitSupervisorArguments(argumentsFile)
	if err != nil {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			err,
		)
	}
	diagnostic.DiscardError(
		"the early Git supervisor has no diagnostic sink",
		"close Git supervisor arguments descriptor",
		argumentsFile.Close(),
	)
	argumentsOpen = false

	if err := unix.Prctl(
		unix.PR_SET_CHILD_SUBREAPER,
		1,
		0,
		0,
		0,
	); err != nil {
		return failedGitSupervisorStatus(
			false,
			1,
			gitSupervisorFailureInternal,
			fmt.Errorf("enable Git child subreaper: %w", err),
		)
	}

	lifetimeEnded := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, readErr := lifetime.Read(buffer[:])
		if errors.Is(readErr, io.EOF) {
			readErr = nil
		}
		lifetimeEnded <- readErr
	}()

	select {
	case lifetimeErr := <-lifetimeEnded:
		if lifetimeErr != nil {
			return failedGitSupervisorStatus(
				false,
				1,
				gitSupervisorFailureInternal,
				fmt.Errorf(
					"read Git supervisor lifetime descriptor: %w",
					lifetimeErr,
				),
			)
		}
		return gitSupervisorStatus{
			Version:  gitSupervisorProtocol,
			Canceled: true,
			ExitCode: 130,
		}
	case current := <-signals:
		return gitSupervisorStatus{
			Version:  gitSupervisorProtocol,
			Canceled: true,
			ExitCode: gitSupervisorSignalExitCode(current),
		}
	default:
	}

	command := arguments.Command
	gitArguments := make([]string, 0, len(command)+1)
	gitArguments = append(
		gitArguments,
		"-C",
		fmt.Sprintf("/proc/self/fd/%d", gitSupervisorRepositoryFD),
	)
	gitArguments = append(gitArguments, command[1:]...)

	gitCommand := exec.Command(command[0], gitArguments...)
	gitCommand.ExtraFiles = []*os.File{repository}
	gitCommand.Env = os.Environ()
	gitCommand.Stdin = os.Stdin
	gitCommand.Stdout = os.Stdout
	gitCommand.Stderr = os.Stderr
	if err := gitCommand.Start(); err != nil {
		failure, code := gitSupervisorStartFailure(err)
		return failedGitSupervisorStatus(false, code, failure, err)
	}

	identity, err := openGitProcessIdentity(
		gitCommand.Process.Pid,
		os.Getpid(),
	)
	if err != nil {
		// The direct child cannot be numerically reused before it is reaped.
		// Process.Kill is only the fail-closed recovery path when the required
		// pidfd could not be retained.
		diagnostic.DiscardError(
			"the primary Git supervisor error is already determined",
			"terminate Git after process identity setup failed",
			gitCommand.Process.Kill(),
		)
		diagnostic.DiscardError(
			"the primary Git supervisor error is already determined",
			"reap Git after process identity setup failed",
			gitCommand.Wait(),
		)
		diagnostic.DiscardError(
			"the primary Git supervisor error is already determined",
			"terminate adopted Git descendants",
			terminateAdoptedGitDescendants(os.Getpid()),
		)
		return failedGitSupervisorStatus(
			true,
			1,
			gitSupervisorFailureInternal,
			err,
		)
	}
	diagnostic.DiscardError(
		"foreground Git owns the terminal",
		"close inherited Git repository descriptor",
		repository.Close(),
	)
	repositoryOpen = false

	wait := make(chan error, 1)
	go func() {
		wait <- gitCommand.Wait()
	}()

	var (
		waitErr      error
		canceled     bool
		cancelCode   int
		superviseErr error
	)
	select {
	case waitErr = <-wait:
	case lifetimeErr := <-lifetimeEnded:
		canceled = true
		cancelCode = 130
		superviseErr = errors.Join(
			identity.Signal(syscall.SIGKILL),
			lifetimeErr,
		)
		waitErr = <-wait
	case current := <-signals:
		canceled = true
		cancelCode = gitSupervisorSignalExitCode(current)
		superviseErr = identity.Signal(syscall.SIGKILL)
		waitErr = <-wait
	}

	diagnostic.DiscardError(
		"the foreground Git result is already determined",
		"close Git process identity",
		identity.Close(),
	)
	diagnostic.DiscardError(
		"the foreground Git result is already determined",
		"terminate adopted Git descendants",
		terminateAdoptedGitDescendants(os.Getpid()),
	)
	if superviseErr != nil {
		return failedGitSupervisorStatus(
			true,
			1,
			gitSupervisorFailureInternal,
			superviseErr,
		)
	}
	if canceled {
		return gitSupervisorStatus{
			Version:  gitSupervisorProtocol,
			Started:  true,
			Canceled: true,
			ExitCode: cancelCode,
		}
	}

	exitCode, err := gitSupervisorCommandExitCode(waitErr)
	if err != nil {
		return failedGitSupervisorStatus(
			true,
			1,
			gitSupervisorFailureInternal,
			err,
		)
	}

	return gitSupervisorStatus{
		Version:  gitSupervisorProtocol,
		Started:  true,
		ExitCode: exitCode,
	}
}

func failedGitSupervisorStatus(
	started bool,
	code int,
	failure gitSupervisorFailure,
	err error,
) gitSupervisorStatus {
	message := "Git supervisor failed"
	if err != nil {
		message = err.Error()
	}

	return gitSupervisorStatus{
		Version:  gitSupervisorProtocol,
		Started:  started,
		ExitCode: code,
		Failure:  failure,
		Error:    message,
	}
}

func gitSupervisorStartFailure(
	err error,
) (gitSupervisorFailure, int) {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return gitSupervisorFailureNotFound, 127
	case errors.Is(err, os.ErrPermission):
		return gitSupervisorFailurePermission, 126
	default:
		return gitSupervisorFailureInternal, 1
	}
}

func gitSupervisorCommandExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 1, fmt.Errorf("wait for Git command: %w", err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok {
		code := exitError.ExitCode()
		if code < 0 || code > 255 {
			return 1, fmt.Errorf("git command has invalid exit code %d", code)
		}
		return code, nil
	}
	if status.Signaled() {
		return 128 + int(status.Signal()), nil
	}

	return status.ExitStatus(), nil
}

func gitSupervisorSignalExitCode(current os.Signal) int {
	signal, ok := current.(syscall.Signal)
	if !ok {
		return 1
	}
	code := 128 + int(signal)
	if code < 0 || code > 255 {
		return 1
	}

	return code
}
