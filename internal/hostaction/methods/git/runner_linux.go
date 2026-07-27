//go:build linux

package git

// Starts the exact Toby executable as a Git subreaper and waits for its exact
// cleanup before returning capture-file ownership to the capability handler.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"petris.dev/toby/internal/diagnostic"
)

type supervisedCommandRunner struct {
	logger *diagnostic.Logger
}

var _ CommandRunner = (*supervisedCommandRunner)(nil)

func newCommandRunner(logger *diagnostic.Logger) CommandRunner {
	return &supervisedCommandRunner{logger: logger}
}

func (r *supervisedCommandRunner) RunHostCommand(
	ctx context.Context,
	directory *os.File,
	command []string,
	stdin *os.File,
	stdout *os.File,
	stderr *os.File,
) (code int, returnErr error) {
	if ctx == nil {
		return 1, fmt.Errorf("run supervised host Git: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return 130, err
	}
	if directory == nil {
		return 1, fmt.Errorf(
			"run supervised host Git: repository descriptor is nil",
		)
	}
	info, err := directory.Stat()
	if err != nil {
		return 1, fmt.Errorf(
			"inspect supervised host Git repository: %w",
			err,
		)
	}
	if !info.IsDir() {
		return 1, fmt.Errorf(
			"run supervised host Git: repository descriptor is not a directory",
		)
	}
	if stdout == nil || stderr == nil {
		return 1, fmt.Errorf(
			"run supervised host Git: output captures must be direct files",
		)
	}

	arguments, err := createGitSupervisorArguments(command)
	if err != nil {
		return 1, err
	}
	argumentsOpen := true
	defer func() {
		if argumentsOpen {
			r.logger.DebugError(
				"close host Git supervisor arguments",
				arguments.Close(),
			)
		}
	}()

	statusFile, err := newGitCapture("supervisor-status", r.logger)
	if err != nil {
		return 1, err
	}
	defer func() {
		r.logger.DebugError(
			"close host Git supervisor status",
			statusFile.Close(),
		)
	}()

	lifetimeReader, lifetimeWriter, err := os.Pipe()
	if err != nil {
		return 1, fmt.Errorf(
			"create Git supervisor lifetime pipe: %w",
			err,
		)
	}
	readerOpen := true
	writerOpen := true
	defer func() {
		if readerOpen {
			r.logger.DebugError(
				"close host Git supervisor lifetime reader",
				lifetimeReader.Close(),
			)
		}
		if writerOpen {
			r.logger.DebugError(
				"close host Git supervisor lifetime writer",
				lifetimeWriter.Close(),
			)
		}
	}()

	supervisor := exec.Command(
		"/proc/self/exe",
		gitSupervisorArgument,
	)
	supervisor.ExtraFiles = []*os.File{
		directory,
		lifetimeReader,
		statusFile,
		arguments,
	}
	supervisor.Env = os.Environ()
	supervisor.Stdin = stdin
	supervisor.Stdout = stdout
	supervisor.Stderr = stderr

	if err := ctx.Err(); err != nil {
		return 130, err
	}
	if err := supervisor.Start(); err != nil {
		return 1, fmt.Errorf("start Git supervisor: %w", err)
	}

	readerErr := lifetimeReader.Close()
	readerOpen = false
	argumentsErr := arguments.Close()
	argumentsOpen = false
	r.logger.DebugError(
		"close host Git supervisor lifetime reader after startup",
		readerErr,
	)
	r.logger.DebugError(
		"close host Git supervisor arguments after startup",
		argumentsErr,
	)

	wait := make(chan error, 1)
	go func() {
		wait <- supervisor.Wait()
	}()

	select {
	case waitErr := <-wait:
		closeErr := lifetimeWriter.Close()
		writerOpen = false
		r.logger.DebugError(
			"close completed host Git supervisor lifetime writer",
			closeErr,
		)
		code, resultErr := finishGitSupervisor(statusFile, waitErr)
		return code, resultErr
	case <-ctx.Done():
		closeErr := lifetimeWriter.Close()
		writerOpen = false
		waitErr := <-wait
		_, resultErr := finishGitSupervisor(statusFile, waitErr)
		r.logger.DebugError(
			"close canceled host Git supervisor lifetime writer",
			closeErr,
		)
		r.logger.DebugError(
			"finish canceled host Git supervisor",
			resultErr,
		)
		return 130, ctx.Err()
	}
}

func finishGitSupervisor(
	statusFile *os.File,
	waitErr error,
) (int, error) {
	processCode, processErr := gitSupervisorCommandExitCode(waitErr)
	status, statusErr := readGitSupervisorStatus(statusFile)
	if processErr != nil || statusErr != nil {
		return 1, errors.Join(processErr, statusErr)
	}
	if status.ExitCode != processCode {
		return 1, fmt.Errorf(
			"git supervisor status %d does not match process status %d",
			status.ExitCode,
			processCode,
		)
	}

	switch status.Failure {
	case gitSupervisorFailureNone:
		return status.ExitCode, nil
	case gitSupervisorFailureNotFound:
		return status.ExitCode, fmt.Errorf(
			"%w: %s",
			os.ErrNotExist,
			status.Error,
		)
	case gitSupervisorFailurePermission:
		return status.ExitCode, fmt.Errorf(
			"%w: %s",
			os.ErrPermission,
			status.Error,
		)
	case gitSupervisorFailureInternal:
		return status.ExitCode, errors.New(status.Error)
	default:
		return 1, fmt.Errorf(
			"unsupported Git supervisor failure %q",
			status.Failure,
		)
	}
}
