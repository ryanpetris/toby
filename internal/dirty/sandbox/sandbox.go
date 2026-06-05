package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/control"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/platform/environ"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

const (
	RuntimeDocker = "docker"
	RuntimeDir    = "/tmp/toby"

	FxRuntimeGroup = "runtimes"
)

// Runtime is a pluggable sandbox backend that creates instances. Today the only
// implementation speaks the Docker Engine API (which also serves Podman); the
// active one is selected by sandbox.runtime in config.
type Runtime interface {
	Name() string
	Priority() int
	Available() error
	NewInstance(Spec) (Instance, error)
}

type Spec struct {
	Label    string
	Projects []Project
	Workdir  string
	Image    string
	Build    tools.Build
}

type Project struct {
	Name     string
	HostPath string
}

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

type Factory struct {
	paths    config.Paths
	runtimes map[string]Runtime
	ordered  []Runtime
}

type factoryParams struct {
	fx.In

	Paths    config.Paths
	Runtimes []Runtime `group:"runtimes"`
}

func provideFactory(params factoryParams) (Factory, error) {
	return newFactory(params.Paths, params.Runtimes)
}

func NewFactory(paths config.Paths, runtimes []Runtime) (Factory, error) {
	return newFactory(paths, runtimes)
}

func newFactory(paths config.Paths, runtimes []Runtime) (Factory, error) {
	items := make(map[string]Runtime, len(runtimes))
	for _, rt := range runtimes {
		if rt == nil || strings.TrimSpace(rt.Name()) == "" {
			return Factory{}, fmt.Errorf("registered sandbox runtime must define a non-empty name")
		}
		if _, exists := items[rt.Name()]; exists {
			return Factory{}, fmt.Errorf("duplicate sandbox runtime: %s", rt.Name())
		}
		items[rt.Name()] = rt
	}
	ordered := make([]Runtime, 0, len(runtimes))
	for _, rt := range runtimes {
		if rt != nil {
			ordered = append(ordered, rt)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority() == ordered[j].Priority() {
			return ordered[i].Name() < ordered[j].Name()
		}
		return ordered[i].Priority() < ordered[j].Priority()
	})
	return Factory{paths: paths, runtimes: items, ordered: ordered}, nil
}

func (f Factory) FromOptions(opts *tools.Options) (Instance, error) {
	if opts == nil {
		opts = &tools.Options{}
	}
	runtime := strings.TrimSpace(opts.SandboxRuntime)
	var rt Runtime
	if runtime == "" {
		selected, err := f.defaultRuntime()
		if err != nil {
			return nil, err
		}
		rt = selected
		runtime = rt.Name()
	} else {
		selected, ok := f.runtimes[runtime]
		if !ok {
			return nil, exitcode.New(2, "unknown sandbox runtime: %s", runtime)
		}
		if err := selected.Available(); err != nil {
			return nil, exitcode.New(2, "sandbox runtime %q is not available: %v", runtime, err)
		}
		rt = selected
	}
	opts.SandboxRuntime = runtime

	spec, err := f.specFromOptions(opts)
	if err != nil {
		return nil, err
	}
	return rt.NewInstance(spec)
}

func (f Factory) defaultRuntime() (Runtime, error) {
	var unavailable []string
	for _, rt := range f.ordered {
		if err := rt.Available(); err != nil {
			unavailable = append(unavailable, fmt.Sprintf("%s: %v", rt.Name(), err))
			continue
		}
		return rt, nil
	}
	if len(unavailable) == 0 {
		return nil, exitcode.New(2, "no sandbox runtimes are registered")
	}
	return nil, exitcode.New(2, "no sandbox runtimes are available: %s", strings.Join(unavailable, "; "))
}

func (f Factory) specFromOptions(opts *tools.Options) (Spec, error) {
	var spec Spec
	var err error
	if len(opts.Projects) > 0 {
		spec, err = f.configuredSpec(opts)
	} else {
		spec, err = f.persistentSpec(opts)
	}
	if err != nil {
		return Spec{}, err
	}
	if opts.SandboxRuntime == RuntimeDocker {
		spec.Image = strings.TrimSpace(opts.Image)
		spec.Build = opts.Build
	}
	return spec, nil
}

func newControlToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func (f Factory) configuredSpec(opts *tools.Options) (Spec, error) {
	env := filepath.ToSlash(strings.TrimSpace(opts.Env))
	if env == "" {
		env = filepath.ToSlash(strings.TrimSpace(opts.Projects[0].Name))
	}
	if err := validateRelativeName("sandbox name", env); err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	projects := make([]Project, 0, len(opts.Projects))
	seen := map[string]bool{}
	for _, configured := range opts.Projects {
		project, err := f.resolveConfiguredProject(configured)
		if err != nil {
			return Spec{}, err
		}
		if seen[project.Name] {
			return Spec{}, exitcode.New(2, "duplicate configured project name: %s", project.Name)
		}
		seen[project.Name] = true
		projects = append(projects, project)
	}
	return Spec{
		Label:    env,
		Projects: projects,
		Workdir:  opts.Workdir,
	}, nil
}

func (f Factory) persistentSpec(opts *tools.Options) (Spec, error) {
	if opts.Env == "" {
		return Spec{}, exitcode.New(2, "environment name is required")
	}
	env := filepath.ToSlash(strings.TrimSpace(opts.Env))
	if err := validateRelativeName("sandbox name", env); err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	projectDir, err := f.resolveProjectDir(env, opts.Project)
	if err != nil {
		return Spec{}, err
	}
	name, err := f.projectName(projectDir)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Label:    env,
		Projects: []Project{{Name: name, HostPath: projectDir}},
		Workdir:  opts.Workdir,
	}, nil
}

func (f Factory) resolveProjectDir(envName, project string) (string, error) {
	var raw string
	switch {
	case project == "":
		if envName == "" {
			return "", nil
		}
		raw = filepath.Join(f.paths.ProjectRoot, envName)
	default:
		raw = config.ExpandHome(project, f.paths.Home)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", exitcode.New(1, "failed to resolve project directory: %s does not exist", raw)
	}
	if _, err := relativeTo(f.paths.ProjectRoot, abs); err != nil {
		return "", exitcode.New(1, "project directory must be under %s: %s", f.paths.ProjectRoot, err)
	}
	return abs, nil
}

func (f Factory) resolveConfiguredProject(project tools.ProjectMount) (Project, error) {
	name := filepath.ToSlash(strings.TrimSpace(project.Name))
	if err := validateRelativeName("project name", name); err != nil {
		return Project{}, exitcode.New(2, "%s", err)
	}
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return Project{}, exitcode.New(2, "configured project %s source is required", name)
	}
	source = config.ExpandHome(source, f.paths.Home)
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return Project{}, exitcode.New(1, "failed to resolve project directory: %s does not exist", source)
	}
	return Project{Name: name, HostPath: source}, nil
}

func (f Factory) projectName(hostPath string) (string, error) {
	name := filepath.Base(hostPath)
	if err := validateRelativeName("project name", name); err != nil {
		return "", err
	}
	return name, nil
}

func validateRelativeName(label, value string) error {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || pathpkg.IsAbs(value) || strings.ContainsRune(value, 0) || strings.Contains(value, "/") {
		return fmt.Errorf("invalid %s: %q", label, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid %s: %q", label, value)
		}
	}
	return nil
}

func relativeTo(base, path string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s must be equal to or inside %s", absPath, absBase)
	}
	return rel, nil
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
