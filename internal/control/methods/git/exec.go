package git

// Low-level Git execution: repository/argument validation, running the git
// binary, and translating process and validation failures into JSON-RPC error
// codes and errnos.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"strings"
	"syscall"

	"petris.dev/toby/internal/control"
)

// ErrProjectNotVisible is returned when a repository is not visible in the sandbox.
var ErrProjectNotVisible = errors.New("repository is not visible in the sandbox")

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

func runGit(ctx context.Context, repository, repoPath string, args []string) Result {
	argv := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Stdin = os.Stdin
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Repository: repository, Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		result.ExitCode = 127
	} else if errors.Is(err, os.ErrPermission) {
		result.ExitCode = 126
	} else if errors.Is(ctx.Err(), context.Canceled) {
		result.ExitCode = 130
	} else {
		result.ExitCode = 1
	}
	if result.Stderr == "" {
		result.Stderr = err.Error()
	}
	return result
}

func errnoFor(err error) error {
	if errors.Is(err, ErrProjectNotVisible) {
		return syscall.EACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return syscall.EIO
}

func rpcErrorCode(err error) int {
	if errors.Is(err, ErrProjectNotVisible) {
		return control.CodeProjectNotVisible
	}
	if errors.Is(err, syscall.EINVAL) {
		return control.CodeInvalidParams
	}
	if errors.Is(err, syscall.ENOSYS) {
		return control.CodeInternalError
	}
	return control.CodeInternalError
}
