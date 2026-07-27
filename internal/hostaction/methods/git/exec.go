package git

// Low-level Git execution: repository/argument validation, running the git
// binary, and translating process and validation failures into JSON-RPC error
// codes and errnos.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/hostaction"
)

// ErrProjectNotVisible is returned when a repository is not visible in the sandbox.
var ErrProjectNotVisible = errors.New("repository is not visible in the sandbox")

// ErrPermissionDenied is returned when the user (or policy) denies the operation.
var ErrPermissionDenied = errors.New("permission denied")

func wrapProjectNotVisible(err error) error {
	return fmt.Errorf("%w: %v", ErrProjectNotVisible, err)
}

func validateRepositoryName(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" || pathpkg.IsAbs(repository) || strings.ContainsRune(repository, 0) {
		return "", syscall.EINVAL
	}
	segments := strings.Split(repository, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", syscall.EINVAL
		}
	}
	return strings.Join(segments, "/"), nil
}

func validateGitArgument(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsRune(value, 0) {
		return "", syscall.EINVAL
	}
	return value, nil
}

func runGit(
	ctx context.Context,
	runner CommandRunner,
	repository string,
	repositoryDirectory *os.File,
	args []string,
	logger *diagnostic.Logger,
) (result Result) {
	result = Result{Repository: repository, ExitCode: 1}
	if runner == nil {
		result.Stderr = "host Git command runner is unavailable"
		return result
	}

	stdout, err := newGitCapture("stdout", logger)
	if err != nil {
		result.Stderr = err.Error()
		return result
	}
	defer func() {
		logger.DebugError("close host Git stdout capture", stdout.Close())
	}()

	stderr, err := newGitCapture("stderr", logger)
	if err != nil {
		result.Stderr = err.Error()
		return result
	}
	defer func() {
		logger.DebugError("close host Git stderr capture", stderr.Close())
	}()

	command := append([]string{"git"}, args...)
	code, runErr := runner.RunHostCommand(
		ctx,
		repositoryDirectory,
		command,
		os.Stdin,
		stdout,
		stderr,
	)
	result.ExitCode = code

	result.Stdout, err = readGitCapture(stdout)
	if err != nil {
		runErr = errors.Join(runErr, err)
	}
	result.Stderr, err = readGitCapture(stderr)
	if err != nil {
		runErr = errors.Join(runErr, err)
	}

	switch {
	case ctx.Err() != nil:
		result.ExitCode = 130
	case errors.Is(runErr, os.ErrNotExist):
		result.ExitCode = 127
	case errors.Is(runErr, os.ErrPermission):
		result.ExitCode = 126
	case runErr != nil && result.ExitCode == 0:
		result.ExitCode = 1
	case result.ExitCode < 0 || result.ExitCode > 255:
		result.ExitCode = 1
	}
	if runErr != nil && result.Stderr == "" {
		result.Stderr = runErr.Error()
	}

	return result
}

func newGitCapture(
	stream string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	if stream == "" || strings.ContainsRune(stream, 0) {
		return nil, fmt.Errorf("invalid Git capture name %q", stream)
	}

	name := "toby-git-" + stream
	flags := unix.MFD_CLOEXEC |
		unix.MFD_ALLOW_SEALING |
		unix.MFD_NOEXEC_SEAL
	descriptor, err := unix.MemfdCreate(name, flags)
	if errors.Is(err, unix.EINVAL) {
		descriptor, err = unix.MemfdCreate(
			name,
			unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING,
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"create anonymous Git %s capture: %w",
			stream,
			err,
		)
	}

	file := os.NewFile(uintptr(descriptor), name)
	if err := file.Chmod(0o600); err != nil {
		logger.DebugError(
			"close invalid host Git capture",
			file.Close(),
		)
		return nil, fmt.Errorf(
			"make anonymous Git %s capture non-executable: %w",
			stream,
			err,
		)
	}

	return file, nil
}

func readGitCapture(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind Git output: %w", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read Git output: %w", err)
	}

	return string(data), nil
}

func errnoFor(err error) error {
	if errors.Is(err, ErrProjectNotVisible) || errors.Is(err, ErrPermissionDenied) {
		return syscall.EACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return syscall.EIO
}

func rpcErrorCode(err error) int {
	if errors.Is(err, ErrPermissionDenied) {
		return hostaction.CodePermissionDenied
	}
	if errors.Is(err, ErrProjectNotVisible) {
		return hostaction.CodeProjectNotVisible
	}
	if errors.Is(err, syscall.EINVAL) {
		return hostaction.CodeInvalidParams
	}
	if errors.Is(err, syscall.ENOSYS) {
		return hostaction.CodeInternalError
	}
	return hostaction.CodeInternalError
}
