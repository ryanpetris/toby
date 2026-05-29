package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"petris.dev/toby/internal/shellquote"
)

type HostMounter interface {
	AddHostPath(string) (string, error)
	VisibleHostPath(string) (string, error)
}

type Confirmer interface {
	ConfirmMount(context.Context, Request) (bool, error)
}

type Request struct {
	Path string
}

type Manager struct {
	Mounter           HostMounter
	Confirmer         Confirmer
	ProjectRoot       string
	MountableProjects bool
	mu                sync.Mutex
}

func (m *Manager) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := DecodeRequest(data)
	if err != nil {
		var syntaxErr *json.SyntaxError
		code := CodeInvalidRequest
		if errors.As(err, &syntaxErr) {
			code = CodeParseError
		}
		return ResponseError(nil, code, err.Error(), nil), syscall.EINVAL
	}
	switch req.Method {
	case "project_list":
		if !m.MountableProjects {
			return ResponseError(req.ID, CodeMountableProjectsDisabled, ErrMountableProjectsDisabled.Error(), nil), syscall.ENOSYS
		}
		result, err := m.projectList()
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "project_readme":
		if !m.MountableProjects {
			return ResponseError(req.ID, CodeMountableProjectsDisabled, ErrMountableProjectsDisabled.Error(), nil), syscall.ENOSYS
		}
		params, err := DecodeProjectParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.projectReadme(params.Name)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "project_mount":
		if !m.MountableProjects {
			return ResponseError(req.ID, CodeMountableProjectsDisabled, ErrMountableProjectsDisabled.Error(), nil), syscall.ENOSYS
		}
		params, err := DecodeProjectParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		path, _, err := m.projectPath(params.Name)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		result, err := m.mount(ctx, path)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "git_commit":
		params, err := DecodeGitCommitParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitCommit(ctx, params.Repository, params.Message)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "git_fetch":
		params, err := DecodeGitRepositoryParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitFetch(ctx, params.Repository)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	case "git_push":
		params, err := DecodeGitPushParams(req.Params)
		if err != nil {
			return ResponseError(req.ID, CodeInvalidParams, err.Error(), nil), syscall.EINVAL
		}
		result, err := m.gitPush(ctx, params.Repository, params.Branch, params.Origin)
		if err != nil {
			return ResponseError(req.ID, rpcErrorCode(err), err.Error(), nil), errnoFor(err)
		}
		return ResponseOK(req.ID, result), nil
	default:
		return ResponseError(req.ID, CodeMethodNotFound, "method not found: "+req.Method, nil), syscall.ENOSYS
	}
}

func (m *Manager) mount(ctx context.Context, path string) (MountResult, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.Contains(path, "\n") {
		return MountResult{}, syscall.EINVAL
	}
	if !filepath.IsAbs(path) {
		return MountResult{}, syscall.EINVAL
	}
	if m.Mounter == nil || m.Confirmer == nil {
		return MountResult{}, syscall.ENOSYS
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	approved, err := m.Confirmer.ConfirmMount(ctx, Request{Path: path})
	if err != nil {
		return MountResult{}, err
	}
	if !approved {
		return MountResult{}, syscall.EACCES
	}
	virtualPath, err := m.Mounter.AddHostPath(path)
	if err != nil {
		return MountResult{}, fmt.Errorf("%w: %v", ErrMountFailed, err)
	}
	return MountResult{HostPath: path, SandboxPath: path, VirtualPath: virtualPath}, nil
}

func (m *Manager) projectList() (ProjectListResult, error) {
	root, err := m.projectRoot()
	if err != nil {
		return ProjectListResult{}, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ProjectListResult{}, err
	}
	projects := []ProjectInfo{}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		projects = append(projects, ProjectInfo{Name: entry.Name(), Path: path})
	}
	return ProjectListResult{ProjectRoot: root, Projects: projects}, nil
}

func (m *Manager) projectReadme(name string) (ProjectReadmeResult, error) {
	projectPath, cleanName, err := m.projectPath(name)
	if err != nil {
		return ProjectReadmeResult{}, err
	}
	readmePath := filepath.Join(projectPath, "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectReadmeResult{}, ErrReadmeNotFound
		}
		return ProjectReadmeResult{}, err
	}
	return ProjectReadmeResult{Name: cleanName, Path: readmePath, Content: string(content)}, nil
}

func (m *Manager) projectPath(name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) || name == "." || name == ".." || strings.ContainsRune(name, os.PathSeparator) {
		return "", "", syscall.EINVAL
	}
	root, err := m.projectRoot()
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(root, name)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", ErrProjectNotFound
		}
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", ErrProjectNotFound
	}
	return path, name, nil
}

func (m *Manager) projectRoot() (string, error) {
	if strings.TrimSpace(m.ProjectRoot) != "" {
		return filepath.Abs(m.ProjectRoot)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := os.Getenv("XDG_PROJECTS_DIR")
	if root == "" {
		root = filepath.Join(home, "Projects")
	}
	return filepath.Abs(expandHome(root, home))
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

type TmuxConfirmer struct{}

var ErrTmuxRequired = errors.New("confirmation requires tmux")

var ErrMountFailed = errors.New("mount failed")

var ErrProjectNotFound = errors.New("project not found")

var ErrReadmeNotFound = errors.New("project README.md not found")

var ErrProjectNotVisible = errors.New("repository is not visible in the sandbox")

var ErrMountableProjectsDisabled = errors.New("mountable projects are disabled")

func (TmuxConfirmer) ConfirmMount(ctx context.Context, req Request) (bool, error) {
	if os.Getenv("TMUX") == "" {
		return false, ErrTmuxRequired
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	popupCommand := shellquote.Join([]string{executable, "__confirm-mount", req.Path})
	cmd := exec.CommandContext(ctx, "tmux", "display-popup", "-E", popupCommand)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("failed to run tmux confirmation: %w", err)
	}
	return true, nil
}

func errnoFor(err error) error {
	if errors.Is(err, ErrTmuxRequired) {
		return syscall.ENOTSUP
	}
	if errors.Is(err, ErrMountFailed) {
		return syscall.EINVAL
	}
	if errors.Is(err, ErrMountableProjectsDisabled) {
		return syscall.ENOSYS
	}
	if errors.Is(err, ErrProjectNotFound) || errors.Is(err, ErrReadmeNotFound) {
		return syscall.ENOENT
	}
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
	if errors.Is(err, ErrTmuxRequired) {
		return CodeTmuxRequired
	}
	if errors.Is(err, syscall.EACCES) {
		return CodeDenied
	}
	if errors.Is(err, ErrMountFailed) {
		return CodeMountFailed
	}
	if errors.Is(err, ErrProjectNotFound) {
		return CodeProjectNotFound
	}
	if errors.Is(err, ErrReadmeNotFound) {
		return CodeReadmeNotFound
	}
	if errors.Is(err, ErrProjectNotVisible) {
		return CodeProjectNotVisible
	}
	if errors.Is(err, ErrMountableProjectsDisabled) {
		return CodeMountableProjectsDisabled
	}
	if errors.Is(err, syscall.EINVAL) {
		return CodeInvalidParams
	}
	if errors.Is(err, syscall.ENOSYS) {
		return CodeInternalError
	}
	return CodeMountFailed
}
