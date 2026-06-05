package mcpproxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/container/engine"

	dstdcopy "github.com/moby/moby/api/pkg/stdcopy"
	dcontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// DockerRunner runs MCP sidecars as containers via testcontainers-go. stdio
// servers are attached to (no TTY — JSON-RPC framing) and demultiplexed into the
// StdioBridge; http servers are reached through their mapped port.
type DockerRunner struct {
	containers *engine.Service

	mu      sync.Mutex
	httpRun map[string]testcontainers.Container
}

func NewDockerRunner(containers *engine.Service) *DockerRunner {
	return &DockerRunner{containers: containers, httpRun: map[string]testcontainers.Container{}}
}

func NewDockerRuntime(containers *engine.Service) RuntimeResult {
	return RuntimeResult{Runtime: NewDockerRunner(containers)}
}

func (r *DockerRunner) Name() RuntimeType { return RuntimeDocker }

func (r *DockerRunner) PrepareStart(spec SidecarSpec) SidecarSpec { return spec }

func (r *DockerRunner) Start(ctx context.Context, spec SidecarSpec) (*ProcessHandle, error) {
	if spec.Transport == TransportHTTP {
		ctr := r.takeHTTP(spec.Name)
		if ctr == nil {
			var err error
			ctr, err = r.containers.Start(ctx, r.httpRequest(spec), r.meta(spec))
			if err != nil {
				return nil, err
			}
		}
		return r.newHandle(ctx, ctr, spec, nil, nil, nil), nil
	}
	return r.startStdio(ctx, spec)
}

func (r *DockerRunner) startStdio(ctx context.Context, spec SidecarSpec) (*ProcessHandle, error) {
	ctr, err := r.containers.Start(ctx, r.stdioRequest(spec), r.meta(spec))
	if err != nil {
		return nil, err
	}
	cli, err := r.containers.Client(ctx)
	if err != nil {
		_ = r.containers.Terminate(ctx, ctr)
		return nil, err
	}
	attach, err := cli.ContainerAttach(ctx, ctr.GetContainerID(), client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		_ = r.containers.Terminate(ctx, ctr)
		return nil, err
	}
	if err := ctr.Start(ctx); err != nil {
		attach.Close()
		_ = r.containers.Terminate(ctx, ctr)
		return nil, err
	}
	// No TTY: the attach stream is Docker-multiplexed. Demux stdout for the
	// JSON-RPC reader and discard stderr (server logs).
	pr, pw := io.Pipe()
	go func() {
		_, _ = dstdcopy.StdCopy(pw, io.Discard, attach.Reader)
		_ = pw.Close()
	}()
	return r.newHandle(ctx, ctr, spec, attach.Conn, pr, func() { attach.Close() }), nil
}

func (r *DockerRunner) stdioRequest(spec SidecarSpec) testcontainers.GenericContainerRequest {
	req := testcontainers.ContainerRequest{
		Image: spec.Image,
		Cmd:   spec.Command,
		Env:   sidecarEnv(spec),
		ConfigModifier: func(c *dcontainer.Config) {
			c.OpenStdin = true
			c.AttachStdin = true
			c.StdinOnce = false
			c.Tty = false
		},
		Labels: map[string]string{"toby.mcp": spec.Name},
	}
	// Started:false so we can attach before the process produces output.
	return testcontainers.GenericContainerRequest{ContainerRequest: req}
}

func (r *DockerRunner) httpRequest(spec SidecarSpec) testcontainers.GenericContainerRequest {
	req := testcontainers.ContainerRequest{
		Image:        spec.Image,
		Cmd:          spec.Command,
		Env:          sidecarEnv(spec),
		ExposedPorts: []string{fmt.Sprintf("%d/tcp", spec.HTTPPort)},
		Labels:       map[string]string{"toby.mcp": spec.Name},
	}
	return testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true}
}

// PrepareHTTP starts the http sidecar eagerly so its mapped port (assigned by
// the daemon at start) can be discovered and turned into a concrete upstream URL
// reachable from the host — including remote/Podman daemons, where a
// pre-allocated loopback port would not work.
func (r *DockerRunner) PrepareHTTP(ctx context.Context, spec SidecarSpec) (string, SidecarSpec, error) {
	if spec.HTTPPort <= 0 {
		return "", spec, fmt.Errorf("mcp.%s.port is required for http transport", spec.Name)
	}
	ctr, err := r.containers.Start(ctx, r.httpRequest(spec), r.meta(spec))
	if err != nil {
		return "", spec, err
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		_ = r.containers.Terminate(ctx, ctr)
		return "", spec, err
	}
	mapped, err := ctr.MappedPort(ctx, fmt.Sprintf("%d/tcp", spec.HTTPPort))
	if err != nil {
		_ = r.containers.Terminate(ctx, ctr)
		return "", spec, err
	}
	r.rememberHTTP(spec.Name, ctr)
	return fmt.Sprintf("http://%s:%s%s", host, mapped.Port(), spec.HTTPPath), spec, nil
}

func (r *DockerRunner) RuntimeInfo(spec SidecarSpec, debug bool) map[string]any {
	info := map[string]any{}
	if spec.Image != "" {
		info["image"] = spec.Image
	}
	if debug && spec.Transport == TransportHTTP {
		info["http"] = map[string]any{"containerPort": spec.HTTPPort, "path": spec.HTTPPath}
	}
	if len(info) == 0 {
		return nil
	}
	return info
}

func (r *DockerRunner) newHandle(ctx context.Context, ctr testcontainers.Container, spec SidecarSpec, stdin io.WriteCloser, stdout io.ReadCloser, cleanup func()) *ProcessHandle {
	handle := &ProcessHandle{stdin: stdin, stdout: stdout, wait: make(chan ProcessResult, 1)}
	if st, err := ctr.State(ctx); err == nil && st != nil {
		handle.pid = st.Pid
	}
	handle.stop = func(ctx context.Context) error {
		if cleanup != nil {
			cleanup()
		}
		if spec.Debug {
			r.containers.Forget(ctr)
			return nil
		}
		return r.containers.Terminate(ctx, ctr)
	}
	go func() {
		handle.wait <- r.waitResult(ctx, ctr)
		close(handle.wait)
	}()
	return handle
}

func (r *DockerRunner) waitResult(ctx context.Context, ctr testcontainers.Container) ProcessResult {
	cli, err := r.containers.Client(ctx)
	if err != nil {
		return ProcessResult{ExitCode: 1, Err: err}
	}
	result := cli.ContainerWait(ctx, ctr.GetContainerID(), client.ContainerWaitOptions{Condition: dcontainer.WaitConditionNotRunning})
	select {
	case res := <-result.Result:
		return ProcessResult{ExitCode: int(res.StatusCode)}
	case werr := <-result.Error:
		return ProcessResult{ExitCode: 1, Err: werr}
	case <-ctx.Done():
		return ProcessResult{ExitCode: 130, Err: ctx.Err()}
	}
}

func (r *DockerRunner) meta(spec SidecarSpec) engine.Meta {
	return engine.Meta{
		Label: spec.Name,
		Kind:  engine.KindMCPSidecar,
		Phase: string(spec.Transport),
		Image: spec.Image,
	}
}

func (r *DockerRunner) rememberHTTP(name string, ctr testcontainers.Container) {
	r.mu.Lock()
	r.httpRun[name] = ctr
	r.mu.Unlock()
}

func (r *DockerRunner) takeHTTP(name string) testcontainers.Container {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctr := r.httpRun[name]
	delete(r.httpRun, name)
	return ctr
}

func sidecarEnv(spec SidecarSpec) map[string]string {
	env := make(map[string]string, len(spec.Env)+1)
	for name, value := range spec.Env {
		env[name] = value
	}
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		env["TERM"] = term
	}
	return env
}
