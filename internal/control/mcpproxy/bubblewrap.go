package mcpproxy

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
)

type BubblewrapRunner struct {
	bwrap string
}

func NewBubblewrapRunner() *BubblewrapRunner {
	path, err := exec.LookPath("bwrap")
	if err != nil || path == "" {
		path = "bwrap"
	}
	return &BubblewrapRunner{bwrap: path}
}

func NewBubblewrapRuntime() RuntimeResult {
	return RuntimeResult{Runtime: NewBubblewrapRunner()}
}

func (r *BubblewrapRunner) Name() RuntimeType { return RuntimeBubblewrap }

func (r *BubblewrapRunner) PrepareStart(spec SidecarSpec) SidecarSpec { return spec }

func (r *BubblewrapRunner) Start(ctx context.Context, spec SidecarSpec) (*ProcessHandle, error) {
	return startProcess(ctx, r.BuildCommand(spec), nil, spec.Transport == TransportStdio, nil)
}

func (r *BubblewrapRunner) BuildCommand(spec SidecarSpec) []string {
	args := []string{
		r.bwrap,
		"--die-with-parent",
		"--unshare-pid",
		"--proc", "/proc",
		"--dev-bind", "/dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/toby",
		"--tmpfs", spec.Home,
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
		"--ro-bind-try", "/run/udev", "/run/udev",
	}
	for _, item := range bubblewrapEnv(sidecarEnv(spec)) {
		args = append(args, "--setenv", item.name, item.value)
	}
	args = append(args, "--chdir", spec.Workdir)
	args = append(args, spec.Command...)
	return args
}

func (r *BubblewrapRunner) PrepareHTTP(_ context.Context, spec SidecarSpec) (string, SidecarSpec, error) {
	spec.HostPort = spec.HTTPPort
	return fmt.Sprintf("http://127.0.0.1:%d%s", spec.HTTPPort, spec.HTTPPath), spec, nil
}

func (r *BubblewrapRunner) RuntimeInfo(spec SidecarSpec, debug bool) map[string]any {
	if !debug || spec.Transport != TransportHTTP {
		return nil
	}
	return map[string]any{"http": map[string]any{"port": spec.HTTPPort, "path": spec.HTTPPath}}
}

type envItem struct {
	name  string
	value string
}

func bubblewrapEnv(env map[string]string) []envItem {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]envItem, 0, len(names))
	for _, name := range names {
		items = append(items, envItem{name: name, value: env[name]})
	}
	return items
}
