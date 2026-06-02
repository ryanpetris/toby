//go:build !darwin

package bubblewrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

type environment struct {
	paths          config.Paths
	runner         executil.Runner
	bwrap          string
	runtime        string
	pipewireCore   string
	waylandDisplay string
	xauthority     string
	available      error
}

type instance struct {
	sandbox.BaseInstance
	runner          executil.Runner
	bwrap           string
	providerRoot    string
	runtimeHostPath string
	runtime         string
	pipewireCore    string
	waylandDisplay  string
	xauthority      string
}

func Module() fx.Option {
	return fx.Module(
		"sandbox.bubblewrap",
		fx.Provide(fx.Annotate(
			newEnvironment,
			fx.ResultTags(`group:"`+sandbox.FxEnvironmentGroup+`"`),
		)),
	)
}

func newEnvironment(paths config.Paths, runner executil.Runner) sandbox.Environment {
	bwrap, err := exec.LookPath("bwrap")
	runtimeDir := bubblewrapRuntimeDir(paths.Home)
	return newBubblewrapEnvironment(paths, runner, bwrap, runtimeDir, envString("PIPEWIRE_CORE", "pipewire-0"), envString("WAYLAND_DISPLAY", "wayland-0"), os.Getenv("XAUTHORITY"), err)
}

func bubblewrapRuntimeDir(home string) string {
	if value := os.Getenv("XDG_RUNTIME_DIR"); value != "" {
		return config.ExpandHome(value, home)
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
}

func newBubblewrapEnvironment(paths config.Paths, runner executil.Runner, bwrap, runtimeDir, pipewireCore, waylandDisplay, xauthority string, available error) *environment {
	if bwrap == "" {
		bwrap = "bwrap"
	}
	return &environment{paths: paths, runner: runner, bwrap: bwrap, runtime: runtimeDir, pipewireCore: pipewireCore, waylandDisplay: waylandDisplay, xauthority: xauthority, available: available}
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (e *environment) Name() string { return sandbox.RuntimeBubblewrap }

func (e *environment) Priority() int { return 1 }

func (e *environment) Available() error { return e.available }

func (e *environment) NewInstance(spec sandbox.Spec) (sandbox.Instance, error) {
	runtimeRoot := filepath.Join(e.runtime, "toby")
	sandboxPaths := sandboxpath.Paths{
		Root:      runtimeRoot,
		Home:      e.paths.Home,
		Context:   filepath.Join(runtimeRoot, "context"),
		Bin:       filepath.Join(runtimeRoot, "bin"),
		Workspace: e.paths.ProjectRoot,
	}
	base, err := sandbox.NewBaseInstance(sandbox.BaseInstanceParams{
		Paths:       e.paths,
		Label:       spec.Label,
		PathSet:     sandboxPaths,
		HomeDir:     e.paths.Home,
		ProjectsDir: e.paths.ProjectRoot,
		RuntimeDir:  runtimeRoot,
		Workdir:     spec.Workdir,
		Projects:    spec.Projects,
	})
	if err != nil {
		return nil, err
	}
	root := spec.BubblewrapRoot
	if root == "" {
		root = e.paths.SandboxRoot
	}
	return &instance{BaseInstance: base, runner: e.runner, bwrap: e.bwrap, providerRoot: root, runtimeHostPath: runtimeRoot, runtime: e.runtime, pipewireCore: e.pipewireCore, waylandDisplay: e.waylandDisplay, xauthority: e.xauthority}, nil
}

func (s *instance) Prime(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if err := s.prepareHostDirs(spec.Mounts); err != nil {
		return 1, err
	}
	return 0, nil
}

func (s *instance) Setup(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if err := s.prepareHostDirs(spec.Mounts); err != nil {
		return 1, err
	}
	cmd, err := s.BuildSetupCommand(spec)
	if err != nil {
		return 1, err
	}
	return s.runner.Run(ctx, cmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
}

func (s *instance) Run(ctx context.Context, spec sandbox.RunSpec) (int, error) {
	if err := s.prepareHostDirs(spec.Mounts); err != nil {
		return 1, err
	}
	cmd, err := s.BuildCommand(spec)
	if err != nil {
		return 1, err
	}
	return s.runner.Run(ctx, cmd, nil, executil.Options{HideOutput: spec.ExecOptions.HideOutput})
}

func (s *instance) prepareHostDirs(mounts []sandboxmount.RuntimeMount) error {
	dirs := []string{
		s.providerRoot,
		s.runtimeHostPath,
		filepath.Join(s.runtimeHostPath, "context"),
		filepath.Join(s.runtimeHostPath, "bin"),
	}
	for _, mount := range mounts {
		if mount.Source.Kind == sandboxmount.SourceProvider {
			dirs = append(dirs, s.providerHostPath(mount))
		}
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *instance) BuildCommand(spec sandbox.RunSpec) ([]string, error) {
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
	for _, name := range sortedEnvNames(spec.Env) {
		args = append(args, "--setenv", name, spec.Env[name])
	}
	args = append(args,
		"--bind", s.runtimeHostPath, s.TobyRuntimeDir(),
		"--bind", "/usr/bin/true", "/usr/bin/xdg-open",
	)
	type finalMount struct {
		target string
		args   []string
	}
	var finalMounts []finalMount
	for _, project := range s.ProjectMounts() {
		finalMounts = append(finalMounts, finalMount{target: project.SandboxPath, args: []string{"--bind", project.HostPath, project.SandboxPath}})
	}
	for _, bind := range spec.Binds {
		target := helpers.ResolvePath(bind.Target, s)
		finalMounts = append(finalMounts, finalMount{target: target, args: []string{bindFlag(bind.Access, bind.Optional), bind.HostPath, target}})
	}
	for _, mount := range spec.Mounts {
		mountArgs := s.mountArgs(mount, false)
		if len(mountArgs) > 0 {
			finalMounts = append(finalMounts, finalMount{target: mount.Target, args: mountArgs})
		}
	}
	sort.SliceStable(finalMounts, func(i, j int) bool {
		return finalMounts[i].target < finalMounts[j].target
	})
	for _, mount := range finalMounts {
		args = append(args, mount.args...)
	}
	args = append(args, "--chdir", s.ChdirDir())
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *instance) BuildSetupCommand(spec sandbox.RunSpec) ([]string, error) {
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
		"--dir", sandboxpath.DefaultRoot,
		"--dir", filepath.ToSlash(filepath.Join(sandboxpath.DefaultRoot, "mounts")),
	}
	for _, name := range sortedEnvNames(spec.Env) {
		args = append(args, "--setenv", name, spec.Env[name])
	}
	args = append(args,
		"--bind", s.runtimeHostPath, s.TobyRuntimeDir(),
	)
	for _, mount := range spec.Mounts {
		args = append(args, s.mountArgs(mount, true)...)
	}
	args = append(args, "--chdir", "/")
	args = append(args, spec.Argv...)
	return args, nil
}

func (s *instance) mountArgs(mount sandboxmount.RuntimeMount, setup bool) []string {
	target := mount.Target
	if setup {
		if mount.Source.Kind != sandboxmount.SourceProvider || mount.SetupPath == "" {
			return nil
		}
		target = mount.SetupPath
	}
	switch mount.Source.Kind {
	case sandboxmount.SourceProvider:
		return []string{bindFlag(mount.Access, mount.Optional), s.providerHostPath(mount), target}
	case sandboxmount.SourceHostPath:
		if setup {
			return nil
		}
		return []string{bindFlag(mount.Access, mount.Optional), mount.Source.Value, target}
	default:
		return nil
	}
}

func (s *instance) providerHostPath(mount sandboxmount.RuntimeMount) string {
	return filepath.Join(s.providerRoot, mount.ProviderID)
}

func sortedEnvNames(env tool.Environment) []string {
	names := make([]string, 0, 3)
	for _, name := range []string{"HOME", control.EnvControlHost, control.EnvControlToken} {
		if _, ok := env[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *instance) runtimeBind(name string) []string {
	if s.runtime == "" || name == "" {
		return nil
	}
	path := filepath.Join(s.runtime, name)
	return []string{"--ro-bind-try", path, path}
}

func bindFlag(kind sandboxmount.Access, optional bool) string {
	suffix := ""
	if optional {
		suffix = "-try"
	}
	switch kind {
	case sandboxmount.AccessRegular, "":
		return "--bind" + suffix
	case sandboxmount.AccessReadOnly:
		return "--ro-bind" + suffix
	case sandboxmount.AccessDev:
		return "--dev-bind" + suffix
	default:
		return "--bind" + suffix
	}
}
