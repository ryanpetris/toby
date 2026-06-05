package runtime

// Instance is one sandbox: the host-side handle to a container through its prime,
// setup, and run phases. BaseInstance provides the runtime-agnostic plumbing
// (paths, control endpoint, project-name → host-path visibility) that the Docker
// instance embeds.

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/control"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
)

type RunSpec struct {
	Argv        []string
	Env         environ.Environment
	Binds       []mount.Bind
	Mounts      []mount.Entry
	ExecOptions sandboxapi.ExecOptions
	Debug       bool
}

type RuntimeInfo struct {
	Runtime string
	Info    map[string]any
}

type Instance interface {
	sandboxapi.Paths

	Label() string
	ProjectPath(string) (string, bool)
	TobyBinDir() string
	TobyBinaryPath() string
	HostControlEndpoint() control.Endpoint
	SetupControlEndpoint(environ.Environment, control.Endpoint)
	Prime(context.Context, RunSpec) (int, error)
	Setup(context.Context, RunSpec) (int, error)
	Run(context.Context, RunSpec) (int, error)
	RuntimeInfo(bool) RuntimeInfo
	Cleanup() error
	VisibleHostPath(string) (string, error)
}

type ProjectMount struct {
	Name        string
	HostPath    string
	SandboxPath string
}

type BaseInstance struct {
	label              string
	controlToken       string
	sandboxControlHost string
	workdir            string
	projects           []ProjectMount
}

type BaseInstanceParams struct {
	Label              string
	ControlToken       string
	SandboxControlHost string
	Workdir            string
	Projects           []Project
}

func NewBaseInstance(params BaseInstanceParams) (BaseInstance, error) {
	controlToken := params.ControlToken
	if controlToken == "" {
		var err error
		controlToken, err = newControlToken()
		if err != nil {
			return BaseInstance{}, err
		}
	}
	return BaseInstance{
		label:              params.Label,
		controlToken:       controlToken,
		sandboxControlHost: params.SandboxControlHost,
		workdir:            params.Workdir,
		projects:           newProjectMounts(params.Projects),
	}, nil
}

func newProjectMounts(projects []Project) []ProjectMount {
	mounts := make([]ProjectMount, 0, len(projects))
	for _, project := range projects {
		mounts = append(mounts, ProjectMount{
			Name:        project.Name,
			HostPath:    project.HostPath,
			SandboxPath: filepath.Join(layout.Workspace, filepath.FromSlash(project.Name)),
		})
	}
	return mounts
}

func (s *BaseInstance) ProjectMounts() []ProjectMount {
	return append([]ProjectMount(nil), s.projects...)
}

func (s *BaseInstance) HomeDir() string { return layout.Home }

func (s *BaseInstance) Label() string { return s.label }

func (s *BaseInstance) Projects() string { return layout.Workspace }

func (s *BaseInstance) ProjectPath(name string) (string, bool) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	for _, project := range s.projects {
		if project.Name == name {
			return project.SandboxPath, true
		}
	}
	return "", false
}

func (s *BaseInstance) TobyRuntimeDir() string {
	return layout.Root
}

func (s *BaseInstance) TobyContextDir() string {
	return layout.Context
}

func (s *BaseInstance) TobyBinDir() string {
	return layout.Bin
}

func (s *BaseInstance) TobyBinaryPath() string {
	return filepath.Join(layout.Bin, "toby")
}

func (s *BaseInstance) TobyOpenCodeConfigDir() string {
	return filepath.Join(layout.Context, "opencode")
}

func (s *BaseInstance) HostControlEndpoint() control.Endpoint {
	return control.WebSocketEndpoint("127.0.0.1:0", s.controlToken)
}

func (s *BaseInstance) SetupControlEndpoint(env environ.Environment, endpoint control.Endpoint) {
	env[control.EnvControlHost] = s.sandboxHost(endpoint.Host)
	env[control.EnvControlToken] = endpoint.Token
}

func (s *BaseInstance) sandboxHost(host string) string {
	if s.sandboxControlHost == "" {
		return host
	}
	old := "127.0.0.1:"
	if strings.HasPrefix(host, old) {
		return strings.Replace(host, old, s.sandboxControlHost+":", 1)
	}
	old = "[::1]:"
	if strings.HasPrefix(host, old) {
		return strings.Replace(host, old, s.sandboxControlHost+":", 1)
	}
	return host
}

func (s *BaseInstance) Cleanup() error {
	return nil
}

func (s *BaseInstance) ChdirDir() string {
	if s.workdir != "" {
		return layout.Expand(s.workdir)
	}
	if len(s.projects) > 0 {
		return s.projects[0].SandboxPath
	}
	return layout.Workspace
}

func (s *BaseInstance) VisibleHostPath(repository string) (string, error) {
	repository, err := repositoryName(repository)
	if err != nil {
		return "", err
	}
	var selected *ProjectMount
	selectedName := ""
	for i := range s.projects {
		if nameWithin(s.projects[i].Name, repository) && len(s.projects[i].Name) > len(selectedName) {
			selected = &s.projects[i]
			selectedName = s.projects[i].Name
		}
	}
	if selected == nil {
		return "", fmt.Errorf("repository is outside visible project: %s", repository)
	}
	rel := strings.TrimPrefix(repository, selected.Name)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := selected.HostPath
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(selected.HostPath, hostPath)
}

func repositoryName(repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" || pathpkg.IsAbs(repository) || strings.ContainsRune(repository, 0) {
		return "", fmt.Errorf("invalid repository name")
	}
	segments := strings.Split(repository, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid repository name")
		}
	}
	return strings.Join(segments, "/"), nil
}

func nameWithin(base, name string) bool {
	return name == base || strings.HasPrefix(name, base+"/")
}

func validateVisibleHostPath(source, hostPath string) (string, error) {
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	resolvedHostPath, err := filepath.EvalSymlinks(hostPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolvedHostPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path is not a directory: %s", hostPath)
	}
	if _, err := relativeTo(resolvedSource, resolvedHostPath); err != nil {
		return "", fmt.Errorf("repository path escapes visible project: %w", err)
	}
	return resolvedHostPath, nil
}
