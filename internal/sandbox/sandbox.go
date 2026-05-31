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

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

const (
	RuntimeBubblewrap = "bubblewrap"
	RuntimeDocker     = "docker"
	RuntimeDir        = "/tmp/toby"

	FxEnvironmentGroup = "toby.sandbox.environments"
)

type Environment interface {
	Name() string
	Priority() int
	Available() error
	NewInstance(Spec) (Instance, error)
}

type EnvironmentResult struct {
	fx.Out

	Environment Environment `group:"toby.sandbox.environments"`
}

type Spec struct {
	Label          string
	Projects       []Project
	Workdir        string
	DockerImage    string
	DockerHome     string
	DockerProjects string
	DockerBuild    tool.DockerBuildConfig
	BubblewrapRoot string
}

type Project struct {
	Name     string
	HostPath string
}

type RunSpec struct {
	Argv        []string
	Toolset     *tool.Toolset
	Env         tool.Environment
	ExecOptions tool.ExecOptions
}

type Instance interface {
	tool.Sandbox

	ProjectPath(string) (string, bool)
	TobyBinDir() string
	TobyBinaryPath() string
	TobyGitAgentsPath() string
	SetupContext(*tool.RunContext)
	HostControlEndpoint() control.Endpoint
	SetupControlEndpoint(tool.Environment, control.Endpoint)
	Run(context.Context, RunSpec) (int, error)
	Cleanup() error
	VisibleHostPath(string) (string, error)
}

type Factory struct {
	paths        config.Paths
	environments map[string]Environment
	ordered      []Environment
}

type FactoryParams struct {
	fx.In

	Paths        config.Paths
	Environments []Environment `group:"toby.sandbox.environments"`
}

func ProvideFactory(params FactoryParams) (Factory, error) {
	return newFactory(params.Paths, params.Environments)
}

func NewFactory(paths config.Paths, runner executil.Runner) Factory {
	factory, err := newFactory(paths, []Environment{
		NewBubblewrapEnvironment(paths, runner),
		NewDockerEnvironment(paths, runner),
	})
	if err != nil {
		panic(err)
	}
	return factory
}

func newFactory(paths config.Paths, environments []Environment) (Factory, error) {
	items := make(map[string]Environment, len(environments))
	for _, env := range environments {
		if env == nil || strings.TrimSpace(env.Name()) == "" {
			return Factory{}, fmt.Errorf("registered sandbox environment must define a non-empty name")
		}
		if _, exists := items[env.Name()]; exists {
			return Factory{}, fmt.Errorf("duplicate sandbox environment: %s", env.Name())
		}
		items[env.Name()] = env
	}
	ordered := make([]Environment, 0, len(environments))
	for _, env := range environments {
		if env != nil {
			ordered = append(ordered, env)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority() == ordered[j].Priority() {
			return ordered[i].Name() < ordered[j].Name()
		}
		return ordered[i].Priority() < ordered[j].Priority()
	})
	return Factory{paths: paths, environments: items, ordered: ordered}, nil
}

func (f Factory) FromOptions(opts *tool.CommandOptions) (Instance, error) {
	if opts == nil {
		opts = &tool.CommandOptions{}
	}
	runtime := strings.TrimSpace(opts.SandboxRuntime)
	var env Environment
	if runtime == "" {
		selected, err := f.defaultEnvironment()
		if err != nil {
			return nil, err
		}
		env = selected
		runtime = env.Name()
	} else {
		selected, ok := f.environments[runtime]
		if !ok {
			return nil, exitcode.New(2, "unknown sandbox runtime: %s", runtime)
		}
		if err := selected.Available(); err != nil {
			return nil, exitcode.New(2, "sandbox runtime %q is not available: %v", runtime, err)
		}
		env = selected
	}
	if runtime != RuntimeDocker && (opts.DockerImage != "" || opts.DockerHome != "" || opts.DockerProjects != "" || opts.DockerBuild.IsSet()) {
		return nil, exitcode.New(2, "docker sandbox settings require sandbox runtime %q", RuntimeDocker)
	}

	spec, err := f.specFromOptions(opts)
	if err != nil {
		return nil, err
	}
	return env.NewInstance(spec)
}

func (f Factory) defaultEnvironment() (Environment, error) {
	var unavailable []string
	for _, env := range f.ordered {
		if err := env.Available(); err != nil {
			unavailable = append(unavailable, fmt.Sprintf("%s: %v", env.Name(), err))
			continue
		}
		return env, nil
	}
	if len(unavailable) == 0 {
		return nil, exitcode.New(2, "no sandbox runtimes are registered")
	}
	return nil, exitcode.New(2, "no sandbox runtimes are available: %s", strings.Join(unavailable, "; "))
}

func (f Factory) specFromOptions(opts *tool.CommandOptions) (Spec, error) {
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
	stateRootBase := f.paths.ProjectRoot
	if len(spec.Projects) > 0 {
		stateRootBase = spec.Projects[0].HostPath
	}
	toolStates, err := opts.ToolStates.ResolveStateRoots(f.paths.Home, stateRootBase)
	if err != nil {
		return Spec{}, exitcode.New(2, "%s", err)
	}
	opts.ToolStates = toolStates
	spec.DockerImage = strings.TrimSpace(opts.DockerImage)
	spec.DockerHome = strings.TrimSpace(opts.DockerHome)
	spec.DockerProjects = strings.TrimSpace(opts.DockerProjects)
	spec.DockerBuild = opts.DockerBuild
	spec.BubblewrapRoot = strings.TrimSpace(opts.BubblewrapRoot)
	return spec, nil
}

func newControlToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func (f Factory) configuredSpec(opts *tool.CommandOptions) (Spec, error) {
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

func (f Factory) persistentSpec(opts *tool.CommandOptions) (Spec, error) {
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

func (f Factory) resolveConfiguredProject(project tool.ProjectMount) (Project, error) {
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

type projectMount struct {
	name        string
	hostPath    string
	sandboxPath string
}

type baseInstance struct {
	paths              config.Paths
	label              string
	homeDir            string
	projectsDir        string
	runtimeDir         string
	controlToken       string
	sandboxControlHost string
	workdir            string
	projects           []projectMount
}

func newProjectMounts(projects []Project, projectsDir string) []projectMount {
	mounts := make([]projectMount, 0, len(projects))
	for _, project := range projects {
		mounts = append(mounts, projectMount{
			name:        project.Name,
			hostPath:    project.HostPath,
			sandboxPath: filepath.Join(projectsDir, filepath.FromSlash(project.Name)),
		})
	}
	return mounts
}

func (s *baseInstance) HomeDir() string { return s.homeDir }

func (s *baseInstance) Projects() string { return s.projectsDir }

func (s *baseInstance) ProjectPath(name string) (string, bool) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	for _, project := range s.projects {
		if project.name == name {
			return project.sandboxPath, true
		}
	}
	return "", false
}

func (s *baseInstance) TobyRuntimeDir() string {
	return s.runtimeDir
}

func (s *baseInstance) TobyContextDir() string {
	return filepath.Join(s.TobyRuntimeDir(), "context")
}

func (s *baseInstance) TobyBinDir() string {
	return filepath.Join(s.TobyRuntimeDir(), "bin")
}

func (s *baseInstance) TobyBinaryPath() string {
	return filepath.Join(s.TobyBinDir(), "toby")
}

func (s *baseInstance) TobyGitAgentsPath() string {
	return filepath.Join(s.TobyContextDir(), "GIT_AGENTS.md")
}

func (s *baseInstance) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.TobyContextDir(), "opencode")
}

func (s *baseInstance) HostControlEndpoint() control.Endpoint {
	return control.WebSocketEndpoint("127.0.0.1:0", s.controlToken)
}

func (s *baseInstance) SetupControlEndpoint(env tool.Environment, endpoint control.Endpoint) {
	env[control.EnvControlHost] = s.sandboxHost(endpoint.Host)
	env[control.EnvControlToken] = endpoint.Token
}

func (s *baseInstance) sandboxHost(host string) string {
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

func (s *baseInstance) Cleanup() error {
	return nil
}

func (s *baseInstance) SetupContext(ctx *tool.RunContext) {
	ctx.Env["HOME"] = s.HomeDir()
	ctx.Env["XDG_PROJECTS_DIR"] = s.Projects()
	ctx.Env["GRML_CHROOT"] = "1"
	ctx.Env["CHROOT"] = "(" + s.label + ")"
	ctx.Env["TOBY_SANDBOX"] = "1"
	ctx.Env["BASH_ENV"] = filepath.Join(s.HomeDir(), ".env")
	delete(ctx.Env, "TOBY_MOUNTABLE_PROJECTS")
	ctx.Env.Prepend("PATH", filepath.Join(s.HomeDir(), ".local", "bin"))
	if ctx.Toolset != nil {
		entries := ctx.Toolset.PathEntries()
		for i := len(entries) - 1; i >= 0; i-- {
			ctx.Env.Prepend("PATH", tool.ResolvePath(entries[i], s))
		}
	}
	ctx.Env.Prepend("PATH", s.TobyBinDir())
}

func (s *baseInstance) chdirDir() string {
	if s.workdir != "" {
		return expandSandboxHome(s.workdir, s.HomeDir())
	}
	if len(s.projects) > 0 {
		return s.projects[0].sandboxPath
	}
	return s.Projects()
}

func expandSandboxHome(value, home string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(value, "~/")))
	}
	return value
}

func (s *baseInstance) VisibleHostPath(repository string) (string, error) {
	repository, err := repositoryName(repository)
	if err != nil {
		return "", err
	}
	var selected *projectMount
	selectedName := ""
	for i := range s.projects {
		if nameWithin(s.projects[i].name, repository) && len(s.projects[i].name) > len(selectedName) {
			selected = &s.projects[i]
			selectedName = s.projects[i].name
		}
	}
	if selected == nil {
		return "", fmt.Errorf("repository is outside visible project: %s", repository)
	}
	rel := strings.TrimPrefix(repository, selected.name)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := selected.hostPath
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(selected.hostPath, hostPath)
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
