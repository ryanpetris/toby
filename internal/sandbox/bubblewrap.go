package sandbox

import (
	"context"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/tool"
)

type BubblewrapEnvironment struct {
	paths  config.Paths
	runner executil.Runner
}

type BubblewrapInstance struct {
	baseInstance
	runner executil.Runner
}

func NewBubblewrapEnvironment(paths config.Paths, runner executil.Runner) *BubblewrapEnvironment {
	return &BubblewrapEnvironment{paths: paths, runner: runner}
}

func ProvideBubblewrapEnvironment(paths config.Paths, runner executil.Runner) EnvironmentResult {
	return EnvironmentResult{Environment: NewBubblewrapEnvironment(paths, runner)}
}

func (e *BubblewrapEnvironment) Name() string { return RuntimeBubblewrap }

func (e *BubblewrapEnvironment) NewInstance(spec Spec) (Instance, error) {
	base := baseInstance{
		paths:                 e.paths,
		label:                 spec.Label,
		homeHostPath:          spec.HomeHostPath,
		homeDir:               e.paths.Home,
		projectsDir:           e.paths.ProjectRoot,
		runtimeDir:            e.paths.XDGRuntimeDir,
		hostControlSocketPath: control.HostSocketPath(e.paths.XDGRuntimeDir, os.Getpid()),
		workdir:               spec.Workdir,
		projects:              newProjectMounts(spec.Projects, e.paths.ProjectRoot),
	}
	return &BubblewrapInstance{baseInstance: base, runner: e.runner}, nil
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

func (s *BubblewrapInstance) BuildCommand(spec RunSpec) ([]string, error) {
	tobyBinary, err := os.Executable()
	if err != nil {
		return nil, err
	}
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
		"--tmpfs", s.runtimeDir,
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
		"--bind", s.homeHostPath, s.HomeDir(),
		"--bind", "/usr/bin/true", "/usr/bin/xdg-open",
		"--ro-bind", tobyBinary, s.TobyBinaryPath(),
		"--bind", s.HostControlSocketPath(), s.TobySandboxSocketPath(),
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

func (s *BubblewrapInstance) runtimeBind(path string) []string {
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
