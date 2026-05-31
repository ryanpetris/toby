package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/tools/tool"
)

type BubblewrapEnvironment struct {
	paths          config.Paths
	runner         executil.Runner
	bwrap          string
	runtime        string
	pipewireCore   string
	waylandDisplay string
	xauthority     string
	available      error
}

type BubblewrapInstance struct {
	baseInstance
	runner         executil.Runner
	bwrap          string
	homeHostPath   string
	runtime        string
	pipewireCore   string
	waylandDisplay string
	xauthority     string
}

func NewBubblewrapEnvironment(paths config.Paths, runner executil.Runner) *BubblewrapEnvironment {
	bwrap, err := exec.LookPath("bwrap")
	runtimeDir := ""
	if value := os.Getenv("XDG_RUNTIME_DIR"); value != "" {
		runtimeDir = config.ExpandHome(value, paths.Home)
	}
	return newBubblewrapEnvironment(paths, runner, bwrap, runtimeDir, envString("PIPEWIRE_CORE", "pipewire-0"), envString("WAYLAND_DISPLAY", "wayland-0"), os.Getenv("XAUTHORITY"), err)
}

func newBubblewrapEnvironment(paths config.Paths, runner executil.Runner, bwrap, runtimeDir, pipewireCore, waylandDisplay, xauthority string, available error) *BubblewrapEnvironment {
	if bwrap == "" {
		bwrap = "bwrap"
	}
	return &BubblewrapEnvironment{paths: paths, runner: runner, bwrap: bwrap, runtime: runtimeDir, pipewireCore: pipewireCore, waylandDisplay: waylandDisplay, xauthority: xauthority, available: available}
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func ProvideBubblewrapEnvironment(paths config.Paths, runner executil.Runner) EnvironmentResult {
	return EnvironmentResult{Environment: NewBubblewrapEnvironment(paths, runner)}
}

func (e *BubblewrapEnvironment) Name() string { return RuntimeBubblewrap }

func (e *BubblewrapEnvironment) Priority() int { return 1 }

func (e *BubblewrapEnvironment) Available() error { return e.available }

func (e *BubblewrapEnvironment) NewInstance(spec Spec) (Instance, error) {
	token, err := newControlToken()
	if err != nil {
		return nil, err
	}
	base := baseInstance{
		paths:        e.paths,
		label:        spec.Label,
		homeDir:      e.paths.Home,
		projectsDir:  e.paths.ProjectRoot,
		runtimeDir:   RuntimeDir,
		controlToken: token,
		workdir:      spec.Workdir,
		projects:     newProjectMounts(spec.Projects, e.paths.ProjectRoot),
	}
	root := spec.BubblewrapRoot
	if root == "" {
		root = e.paths.SandboxRoot
	}
	return &BubblewrapInstance{baseInstance: base, runner: e.runner, bwrap: e.bwrap, homeHostPath: filepath.Join(root, filepath.FromSlash(spec.Label)), runtime: e.runtime, pipewireCore: e.pipewireCore, waylandDisplay: e.waylandDisplay, xauthority: e.xauthority}, nil
}

func (s *BubblewrapInstance) Run(ctx context.Context, spec RunSpec) (int, error) {
	if err := s.prepareHostDirs(); err != nil {
		return 1, err
	}
	cmd, err := s.BuildCommand(spec)
	if err != nil {
		return 1, err
	}
	return s.runner.Run(ctx, cmd, spec.Env, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
}

func (s *BubblewrapInstance) prepareHostDirs() error {
	return os.MkdirAll(s.homeHostPath, 0o755)
}

func (s *BubblewrapInstance) BuildCommand(spec RunSpec) ([]string, error) {
	args := []string{
		s.bwrap,
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
		"--ro-bind-try", "/run/systemd/resolve", "/run/systemd/resolve",
	}
	args = append(args, s.runtimeBind("pulse")...)
	args = append(args, s.runtimeBind(s.pipewireCore)...)
	args = append(args, "--ro-bind-try", "/run/udev", "/run/udev")
	args = append(args, s.runtimeBind(s.waylandDisplay)...)
	args = append(args,
		"--ro-bind-try", "/tmp/.ICE-unix", "/tmp/.ICE-unix",
		"--ro-bind-try", "/tmp/.X11-unix", "/tmp/.X11-unix",
	)
	if s.xauthority != "" {
		args = append(args, "--ro-bind-try", s.xauthority, s.xauthority)
	}
	args = append(args,
		"--bind", s.homeHostPath, s.HomeDir(),
		"--bind", "/usr/bin/true", "/usr/bin/xdg-open",
	)
	for _, project := range s.projects {
		args = append(args, "--bind", project.hostPath, project.sandboxPath)
	}
	if spec.Toolset != nil {
		for _, bind := range spec.Toolset.Binds() {
			args = append(args, bindFlag(bind.Type, bind.Optional), bind.HostPath, tool.ResolvePath(bind.Target, s))
		}
	}
	args = append(args, "--chdir", s.chdirDir())
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *BubblewrapInstance) runtimeBind(name string) []string {
	if s.runtime == "" || name == "" {
		return nil
	}
	path := filepath.Join(s.runtime, name)
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
