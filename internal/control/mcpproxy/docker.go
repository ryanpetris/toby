package mcpproxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

type DockerRunner struct {
	docker string
}

func NewDockerRunner() *DockerRunner {
	path, err := exec.LookPath("docker")
	if err != nil || path == "" {
		path = "docker"
	}
	return &DockerRunner{docker: path}
}

func NewDockerRuntime() RuntimeResult {
	return RuntimeResult{Runtime: NewDockerRunner()}
}

func (r *DockerRunner) Name() RuntimeType { return RuntimeDocker }

func (r *DockerRunner) PrepareStart(spec SidecarSpec) SidecarSpec {
	spec.ContainerName = containerName(spec.Name)
	return spec
}

func (r *DockerRunner) Start(ctx context.Context, spec SidecarSpec) (*ProcessHandle, error) {
	argv := r.BuildCommand(spec)
	stop := func(context.Context) error {
		if spec.ContainerName == "" {
			return nil
		}
		if spec.Debug {
			return exec.Command(r.docker, "stop", spec.ContainerName).Run()
		}
		return exec.Command(r.docker, "rm", "-f", spec.ContainerName).Run()
	}
	return startProcess(ctx, argv, nil, spec.Transport == TransportStdio, stop)
}

func (r *DockerRunner) BuildCommand(spec SidecarSpec) []string {
	args := []string{r.docker, "run"}
	if !spec.Debug {
		args = append(args, "--rm")
	}
	if spec.Transport == TransportStdio {
		args = append(args, "-i")
	}
	if spec.ContainerName != "" {
		args = append(args, "--name", spec.ContainerName)
	}
	if spec.Transport == TransportHTTP && spec.HTTPPort > 0 && spec.HostPort > 0 {
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", spec.HostPort, spec.HTTPPort))
	}
	for _, item := range dockerEnv(spec.Env) {
		args = append(args, "--env", item)
	}
	args = appendHostTermEnv(args)
	args = append(args, spec.DockerImage)
	args = append(args, spec.Command...)
	return args
}

func (r *DockerRunner) PrepareHTTP(ctx context.Context, spec SidecarSpec) (string, SidecarSpec, error) {
	allocated, err := allocateLoopbackPort(ctx)
	if err != nil {
		return "", spec, err
	}
	spec.HostPort = allocated
	return fmt.Sprintf("http://127.0.0.1:%d%s", allocated, spec.HTTPPath), spec, nil
}

func (r *DockerRunner) RuntimeInfo(spec SidecarSpec, debug bool) map[string]any {
	info := map[string]any{}
	if spec.DockerImage != "" {
		info["image"] = spec.DockerImage
	}
	if debug {
		if spec.ContainerName != "" {
			info["container"] = map[string]any{"name": spec.ContainerName}
		}
		if spec.Transport == TransportHTTP {
			info["http"] = map[string]any{"containerPort": spec.HTTPPort, "hostPort": spec.HostPort, "path": spec.HTTPPath}
		}
	}
	if len(info) == 0 {
		return nil
	}
	return info
}

func dockerEnv(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name+"="+env[name])
	}
	return values
}

func appendHostTermEnv(args []string) []string {
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		args = append(args, "--env", "TERM="+term)
	}
	return args
}
