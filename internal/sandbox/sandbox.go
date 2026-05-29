package sandbox

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

type Factory struct {
	paths  config.Paths
	runner executil.Runner
}

func NewFactory(paths config.Paths, runner executil.Runner) Factory {
	return Factory{paths: paths, runner: runner}
}

type Sandbox struct {
	paths          config.Paths
	runner         executil.Runner
	label          string
	homeDir        string
	projectDir     string
	sandboxProjDir string
	workdir        string
	projectMounts  []projectMount
	tempHome       string
}

type projectMount struct {
	name       string
	sourceDir  string
	sandboxDir string
}

type bwrapBind struct {
	HostPath    string
	SandboxPath string
	Type        tool.BindType
	Optional    bool
}

type CommandMounts struct {
	Binds         []bwrapBind
	ControlSocket string
	TobyBinary    string
}

func (s *Sandbox) CommandMounts(toolset *tool.Toolset, controlSocket, tobyBinary string) CommandMounts {
	return CommandMounts{Binds: bwrapBindsForToolset(toolset), ControlSocket: controlSocket, TobyBinary: tobyBinary}
}

func (f Factory) FromOptions(opts *tool.CommandOptions) (*Sandbox, error) {
	if len(opts.Projects) > 0 {
		return f.fromConfiguredEnvironment(opts)
	}
	if opts.TmpEnv {
		return f.fromTemporaryEnvironment(opts)
	}
	return f.fromPersistentEnvironment(opts)
}

func (f Factory) fromConfiguredEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.TmpEnv {
		return nil, exitcode.New(2, "configured projects cannot be used with --tmp-env")
	}
	if err := os.MkdirAll(f.paths.SandboxRoot, 0o755); err != nil {
		return nil, err
	}
	env := filepath.ToSlash(strings.TrimSpace(opts.Env))
	if env == "" {
		env = filepath.ToSlash(strings.TrimSpace(opts.Projects[0].Name))
	}
	if err := validateRelativeName("sandbox name", env); err != nil {
		return nil, exitcode.New(2, "%s", err)
	}
	mounts := make([]projectMount, 0, len(opts.Projects))
	seen := map[string]bool{}
	for _, configured := range opts.Projects {
		mount, err := f.resolveConfiguredProjectMount(configured)
		if err != nil {
			return nil, err
		}
		if seen[mount.name] {
			return nil, exitcode.New(2, "duplicate configured project name: %s", mount.name)
		}
		seen[mount.name] = true
		mounts = append(mounts, mount)
	}
	primary := mounts[0]
	return &Sandbox{
		paths:          f.paths,
		runner:         f.runner,
		label:          env,
		homeDir:        filepath.Join(f.paths.SandboxRoot, filepath.FromSlash(env)),
		projectDir:     primary.sourceDir,
		sandboxProjDir: primary.sandboxDir,
		workdir:        opts.Workdir,
		projectMounts:  mounts,
	}, nil
}

func (f Factory) fromTemporaryEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.Project != "" && opts.Env != "" {
		return nil, exitcode.New(2, "tmp env project specified twice: use either positional PROJECT or --project DIR")
	}
	if opts.Env == "" && opts.Project == "" {
		return nil, exitcode.New(2, "tmp env requires a project")
	}

	tempHome, err := os.MkdirTemp("", "toby-")
	if err != nil {
		return nil, err
	}
	projectName := opts.Project
	if projectName == "" {
		projectName = opts.Env
	}
	projectDir, err := f.resolveProjectDir("", projectName, true)
	if err != nil {
		_ = os.RemoveAll(tempHome)
		return nil, err
	}
	sandboxProjDir := projectDir
	if sandboxProjDir == "" {
		sandboxProjDir = filepath.Join(f.paths.ProjectRoot, filepath.Base(tempHome))
	}
	return &Sandbox{
		paths:          f.paths,
		runner:         f.runner,
		label:          "tmp",
		homeDir:        tempHome,
		projectDir:     projectDir,
		sandboxProjDir: sandboxProjDir,
		workdir:        opts.Workdir,
		tempHome:       tempHome,
	}, nil
}

func (f Factory) fromPersistentEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.Env == "" {
		return nil, exitcode.New(2, "environment name is required unless --tmp-env is used")
	}
	if err := os.MkdirAll(f.paths.SandboxRoot, 0o755); err != nil {
		return nil, err
	}
	projectDir, err := f.resolveProjectDir(opts.Env, opts.Project, false)
	if err != nil {
		return nil, err
	}
	sandboxProjDir := projectDir
	if sandboxProjDir == "" {
		sandboxProjDir = filepath.Join(f.paths.ProjectRoot, opts.Env)
	}
	return &Sandbox{
		paths:          f.paths,
		runner:         f.runner,
		label:          opts.Env,
		homeDir:        filepath.Join(f.paths.SandboxRoot, opts.Env),
		projectDir:     projectDir,
		sandboxProjDir: sandboxProjDir,
		workdir:        opts.Workdir,
	}, nil
}

func (f Factory) resolveProjectDir(envName, project string, tmpEnv bool) (string, error) {
	var raw string
	switch {
	case project == "":
		if envName == "" {
			return "", nil
		}
		raw = filepath.Join(f.paths.ProjectRoot, envName)
	case tmpEnv && isProjectShorthand(project):
		raw = filepath.Join(f.paths.ProjectRoot, project)
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

func (f Factory) resolveConfiguredProjectMount(project tool.ProjectMount) (projectMount, error) {
	name := filepath.ToSlash(strings.TrimSpace(project.Name))
	if err := validateRelativeName("project name", name); err != nil {
		return projectMount{}, exitcode.New(2, "%s", err)
	}
	source := strings.TrimSpace(project.Source)
	if source == "" {
		return projectMount{}, exitcode.New(2, "configured project %s source is required", name)
	}
	source = config.ExpandHome(source, f.paths.Home)
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return projectMount{}, exitcode.New(1, "failed to resolve project directory: %s does not exist", source)
	}
	return projectMount{
		name:       name,
		sourceDir:  source,
		sandboxDir: filepath.Join(f.paths.ProjectRoot, filepath.FromSlash(name)),
	}, nil
}

func validateRelativeName(label, value string) error {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || pathpkg.IsAbs(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid %s: %q", label, value)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid %s: %q", label, value)
		}
	}
	return nil
}

func isProjectShorthand(project string) bool {
	if project == "." || project == ".." || filepath.IsAbs(project) {
		return false
	}
	return !strings.ContainsRune(project, os.PathSeparator)
}

func (s *Sandbox) HomeDir() string { return s.homeDir }

func (s *Sandbox) ProjectRoot() string { return s.paths.ProjectRoot }

func (s *Sandbox) OpenCodeConfigDir() string {
	return filepath.Join(s.paths.SandboxRoot, ".config", "opencode")
}

func (s *Sandbox) TobyRuntimeDir() string {
	return filepath.Join(s.paths.XDGRuntimeDir, "toby")
}

func (s *Sandbox) TobyContextDir() string {
	return filepath.Join(s.TobyRuntimeDir(), "context")
}

func (s *Sandbox) TobyBinDir() string {
	return filepath.Join(s.TobyRuntimeDir(), "bin")
}

func (s *Sandbox) TobyBinaryPath() string {
	return filepath.Join(s.TobyBinDir(), "toby")
}

func (s *Sandbox) TobySandboxSocketPath() string {
	return filepath.Join(s.TobyRuntimeDir(), control.SandboxSocketName)
}

func (s *Sandbox) TobyGitAgentsPath() string {
	return filepath.Join(s.TobyContextDir(), "GIT_AGENTS.md")
}

func (s *Sandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.TobyContextDir(), "opencode")
}

func (s *Sandbox) Cleanup() error {
	if s.tempHome == "" {
		return nil
	}
	tempHome := s.tempHome
	s.tempHome = ""
	return os.RemoveAll(tempHome)
}

func (s *Sandbox) EnsureHome() error {
	return os.MkdirAll(s.homeDir, 0o755)
}

func (s *Sandbox) EnsureSandboxProjectDir() error {
	if len(s.projectMounts) > 0 {
		return nil
	}
	if s.projectDir != "" {
		return nil
	}
	source, err := s.sandboxProjectSourceDir()
	if err != nil {
		return exitcode.New(1, "failed to create sandbox project directory: %s; set XDG_PROJECTS_DIR inside %s or provide --project", err, s.paths.Home)
	}
	return os.MkdirAll(source, 0o755)
}

func (s *Sandbox) sandboxProjectSourceDir() (string, error) {
	rel, err := relativeTo(s.paths.Home, s.sandboxProjDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.homeDir, rel), nil
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

func (s *Sandbox) SetupContext(ctx *tool.RunContext) {
	ctx.Env["XDG_RUNTIME_DIR"] = s.paths.XDGRuntimeDir
	ctx.Env["GRML_CHROOT"] = "1"
	ctx.Env["CHROOT"] = "(" + s.label + ")"
	ctx.Env["TOBY_SANDBOX"] = "1"
	ctx.Env["BASH_ENV"] = filepath.Join(s.paths.Home, ".env")
	delete(ctx.Env, "TOBY_MOUNTABLE_PROJECTS")
	ctx.Env.Prepend("PATH", filepath.Join(s.paths.Home, ".local", "bin"))
	entries := ctx.Toolset.PathEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		ctx.Env.Prepend("PATH", entries[i])
	}
	ctx.Env.Prepend("PATH", s.TobyBinDir())
}

func (s *Sandbox) BuildCommand(argv []string, mounts CommandMounts) []string {
	args := []string{
		"/usr/bin/bwrap",
		"--die-with-parent",
		"--unshare-pid",
		"--proc", "/proc",
		"--dev-bind", "/dev", "/dev",
		"--tmpfs", "/tmp",
		"--ro-bind-try", "/etc", "/etc",
		"--ro-bind-try", "/opt", "/opt",
		"--bind-try", "/sys", "/sys",
		"--ro-bind-try", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/bin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib", "/lib64",
		"--ro-bind-try", "/var/empty", "/var/empty",
		"--tmpfs", s.paths.XDGRuntimeDir,
		"--dir", s.TobyRuntimeDir(),
		"--dir", s.TobyBinDir(),
		"--ro-bind-try", "/run/systemd/resolve", "/run/systemd/resolve",
	}
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, "pulse"))...)
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, s.paths.PipewireCore))...)
	args = append(args, "--ro-bind-try", "/run/udev", "/run/udev")
	args = append(args, s.runtimeBind(filepath.Join(s.paths.XDGRuntimeDir, s.paths.WaylandDisplay))...)
	args = append(args,
		"--ro-bind-try", "/tmp/.ICE-unix", "/tmp/.ICE-unix",
		"--ro-bind-try", "/tmp/.X11-unix", "/tmp/.X11-unix",
	)
	if s.paths.XAuthority != "" {
		args = append(args, "--ro-bind-try", s.paths.XAuthority, s.paths.XAuthority)
	}
	args = append(args,
		"--bind", s.homeDir, s.paths.Home,
		"--bind", "/usr/bin/true", "/usr/bin/xdg-open",
	)
	if mounts.TobyBinary != "" {
		args = append(args, "--ro-bind", mounts.TobyBinary, s.TobyBinaryPath())
	}
	if mounts.ControlSocket != "" {
		args = append(args, "--bind", mounts.ControlSocket, s.TobySandboxSocketPath())
	}
	if len(s.projectMounts) > 0 {
		for _, mount := range s.projectMounts {
			args = append(args, "--bind", mount.sourceDir, mount.sandboxDir)
		}
	} else if s.projectDir != "" {
		args = append(args, "--bind", s.projectDir, s.sandboxProjDir)
	}
	for _, bind := range mounts.Binds {
		args = append(args, bindFlag(bind.Type, bind.Optional), bind.HostPath, bind.SandboxPath)
	}
	args = append(args, "--chdir", s.chdirDir())
	args = append(args, argv...)
	return args
}

func (s *Sandbox) chdirDir() string {
	if s.workdir != "" {
		return s.workdir
	}
	return s.sandboxProjDir
}

func bwrapBindsForToolset(toolset *tool.Toolset) []bwrapBind {
	if toolset == nil {
		return nil
	}
	binds := toolset.Binds()
	result := make([]bwrapBind, 0, len(binds))
	for _, bind := range binds {
		result = append(result, bwrapBind(bind))
	}
	return result
}

func (s *Sandbox) VisibleHostPath(repository string) (string, error) {
	if len(s.projectMounts) > 0 {
		return s.visibleConfiguredHostPath(repository)
	}
	virtual, err := repositoryVirtualPath(repository)
	if err != nil {
		return "", err
	}
	base, err := projectVirtualPath(s.paths.ProjectRoot, s.sandboxProjDir)
	if err != nil {
		return "", err
	}
	if !virtualPathWithin(base, virtual) {
		return "", fmt.Errorf("repository is outside visible project: %s", repository)
	}
	source := s.projectDir
	if source == "" {
		source, err = s.sandboxProjectSourceDir()
		if err != nil {
			return "", err
		}
	}
	rel := strings.TrimPrefix(virtual, base)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := source
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(source, hostPath)
}

func (s *Sandbox) visibleConfiguredHostPath(repository string) (string, error) {
	virtual, err := repositoryVirtualPath(repository)
	if err != nil {
		return "", err
	}
	var selected *projectMount
	selectedBase := ""
	for i := range s.projectMounts {
		base, err := projectVirtualPath(s.paths.ProjectRoot, s.projectMounts[i].sandboxDir)
		if err != nil {
			return "", err
		}
		if virtualPathWithin(base, virtual) && len(base) > len(selectedBase) {
			selected = &s.projectMounts[i]
			selectedBase = base
		}
	}
	if selected == nil {
		return "", fmt.Errorf("repository is outside visible project: %s", repository)
	}
	rel := strings.TrimPrefix(virtual, selectedBase)
	rel = strings.TrimPrefix(rel, "/")
	hostPath := selected.sourceDir
	if rel != "" {
		hostPath = filepath.Join(hostPath, filepath.FromSlash(rel))
	}
	return validateVisibleHostPath(selected.sourceDir, hostPath)
}

func projectVirtualPath(projectRoot, path string) (string, error) {
	rel, err := relativeTo(projectRoot, path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "/projects", nil
	}
	return "/projects/" + filepath.ToSlash(rel), nil
}

func repositoryVirtualPath(repository string) (string, error) {
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
	return "/projects/" + strings.Join(segments, "/"), nil
}

func virtualPathWithin(base, path string) bool {
	return path == base || strings.HasPrefix(path, base+"/")
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

func (s *Sandbox) runtimeBind(path string) []string {
	return []string{"--ro-bind-try", path, path}
}

func bindFlag(kind tool.BindType, optional bool) string {
	suffix := ""
	if optional {
		suffix = "-try"
	}
	switch kind {
	case tool.BindRegular, "":
		return "--bind" + suffix
	case tool.BindReadOnly:
		return "--ro-bind" + suffix
	case tool.BindDev:
		return "--dev-bind" + suffix
	default:
		return "--bind" + suffix
	}
}

func (s *Sandbox) Run(ctx context.Context, argv []string, mounts CommandMounts, env tool.Environment, opts tool.ExecOptions) (int, error) {
	cmd := s.BuildCommand(argv, mounts)
	return s.runner.Run(ctx, cmd, env, executil.Options{HideOutput: opts.HideOutput})
}
