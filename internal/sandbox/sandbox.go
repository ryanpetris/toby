package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
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
	tempHome       string
}

func (f Factory) FromOptions(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.TmpEnv {
		return f.fromTemporaryEnvironment(opts)
	}
	return f.fromPersistentEnvironment(opts)
}

func (f Factory) fromTemporaryEnvironment(opts *tool.CommandOptions) (*Sandbox, error) {
	if opts.Project != "" && opts.Env != "" {
		return nil, exitcode.New(2, "tmp env project specified twice: use either positional PROJECT or --project DIR")
	}
	if opts.NoProject && opts.Env != "" {
		return nil, exitcode.New(2, "cannot provide a positional project when --no-project is set")
	}
	if opts.Env == "" && opts.Project == "" && !opts.NoProject {
		return nil, exitcode.New(2, "tmp env requires a project unless --no-project is set")
	}

	tempHome, err := os.MkdirTemp("", "toby-")
	if err != nil {
		return nil, err
	}
	projectName := opts.Project
	if projectName == "" {
		projectName = opts.Env
	}
	projectDir, err := f.resolveProjectDir("", projectName, opts.NoProject, true)
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
	projectDir, err := f.resolveProjectDir(opts.Env, opts.Project, opts.NoProject, false)
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
	}, nil
}

func (f Factory) resolveProjectDir(envName, project string, noProject, tmpEnv bool) (string, error) {
	if noProject {
		return "", nil
	}
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
	return abs, nil
}

func isProjectShorthand(project string) bool {
	if project == "." || project == ".." || filepath.IsAbs(project) {
		return false
	}
	return !strings.ContainsRune(project, os.PathSeparator)
}

func (s *Sandbox) HomeDir() string { return s.homeDir }

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
	if s.projectDir != "" {
		return nil
	}
	rel, err := relativeTo(s.paths.Home, s.sandboxProjDir)
	if err != nil {
		return exitcode.New(1, "failed to create sandbox project directory: %s; set XDG_PROJECTS_DIR inside %s or provide --project", err, s.paths.Home)
	}
	return os.MkdirAll(filepath.Join(s.homeDir, rel), 0o755)
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
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s must be inside %s", absPath, absBase)
	}
	return rel, nil
}

func (s *Sandbox) SetupContext(ctx *tool.RunContext) {
	ctx.Env["GRML_CHROOT"] = "1"
	ctx.Env["CHROOT"] = "(" + s.label + ")"
	ctx.Env["BASH_ENV"] = filepath.Join(s.paths.Home, ".env")
	ctx.Env.Prepend("PATH", filepath.Join(s.paths.Home, ".local", "bin"))
	entries := ctx.Toolset.PathEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		ctx.Env.Prepend("PATH", entries[i])
	}
}

func (s *Sandbox) BuildCommand(argv []string, toolset *tool.Toolset) []string {
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
	for _, bind := range toolset.Binds() {
		args = append(args, bindFlag(bind.Type, bind.Optional), bind.HostPath, bind.SandboxPath)
	}
	if s.projectDir != "" {
		args = append(args, "--bind-try", s.projectDir, s.projectDir)
	}
	args = append(args, "--chdir", s.sandboxProjDir)
	args = append(args, argv...)
	return args
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

func (s *Sandbox) Run(ctx context.Context, argv []string, toolset *tool.Toolset, env tool.Environment, opts tool.ExecOptions) (int, error) {
	cmd := s.BuildCommand(argv, toolset)
	return s.runner.Run(ctx, cmd, env, executil.Options{HideOutput: opts.HideOutput})
}
