package control

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
)

func (m *Manager) gitCommit(ctx context.Context, repository, message string) (GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return GitResult{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return GitResult{}, syscall.EINVAL
	}
	return m.runVisibleGit(ctx, repository, []string{"commit", "-m", message})
}

func (m *Manager) gitFetch(ctx context.Context, repository string) (GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return GitResult{}, err
	}
	return m.runVisibleGit(ctx, repository, []string{"fetch"})
}

func (m *Manager) gitPush(ctx context.Context, repository, branch, origin string) (GitResult, error) {
	repository, err := validateRepositoryName(repository)
	if err != nil {
		return GitResult{}, err
	}
	branch, err = validateGitArgument(branch)
	if err != nil {
		return GitResult{}, err
	}
	if strings.TrimSpace(origin) == "" {
		origin = "origin"
	} else {
		origin, err = validateGitArgument(origin)
		if err != nil {
			return GitResult{}, err
		}
	}
	return m.runVisibleGit(ctx, repository, []string{"push", origin, branch})
}

func (m *Manager) runVisibleGit(ctx context.Context, repository string, args []string) (GitResult, error) {
	if m.RepositoryResolver == nil {
		return GitResult{}, syscall.ENOSYS
	}
	repoPath, err := m.RepositoryResolver.VisibleHostPath(repository)
	if err != nil {
		return GitResult{}, fmt.Errorf("%w: %v", ErrProjectNotVisible, err)
	}
	return runGit(ctx, repository, repoPath, args), nil
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

func runGit(ctx context.Context, repository, repoPath string, args []string) GitResult {
	argv := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Stdin = os.Stdin
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := GitResult{Repository: repository, Stdout: stdout.String(), Stderr: stderr.String()}
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
