package runtime

// The Docker-backed Instance: it creates the single long-lived container, delivers
// the toby binary with docker cp, starts it on an idle command, then launches the
// proxy-only manager as a docker exec and hands back the host side of that exec's
// stdio gRPC link. Keeping the manager off the container's main process leaves
// `docker logs` empty instead of full of gRPC frames. Tools, mount-init, and file
// provisioning then run against the live container via docker exec / docker cp.
// Building images shells out to the docker CLI (see build.go); everything else
// goes through the moby client.

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/control/stdio"
	sandboxbinary "petris.dev/toby/internal/sandbox/binary"
	"petris.dev/toby/tools"

	dstdcopy "github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// DefaultImage is the image used when no image or build is configured.
const DefaultImage = "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm"

type instance struct {
	BaseInstance
	containers   *engine.Service
	image        string
	build        tools.Build
	exposedPorts network.PortSet
	portBindings network.PortMap

	mu           sync.Mutex
	runContainer testcontainers.Container
}

var _ Instance = (*instance)(nil)

func (s *instance) RuntimeInfo(debug bool) RuntimeInfo {
	info := map[string]any{
		"image": s.image,
	}
	if debug && s.build.IsSet() {
		info["build"] = map[string]any{"context": s.build.Context, "dockerfile": s.build.Dockerfile}
	}
	if debug {
		var tracked []map[string]any
		for _, c := range s.containers.Snapshot() {
			if c.Kind != engine.KindSandbox {
				continue
			}
			tracked = append(tracked, map[string]any{
				"id":      c.ID,
				"phase":   c.Phase,
				"image":   c.Image,
				"network": c.Network,
			})
		}
		if len(tracked) > 0 {
			info["containers"] = tracked
		}
	}
	return RuntimeInfo{Runtime: "docker", Info: info}
}

// Cleanup defensively terminates the long-lived Run container if it is still
// tracked (e.g. an early return skipped the normal teardown).
func (s *instance) Cleanup() error {
	s.mu.Lock()
	ctr := s.runContainer
	s.runContainer = nil
	s.mu.Unlock()
	if ctr != nil {
		_ = s.containers.Terminate(context.Background(), ctr)
	}
	return nil
}

func (s *instance) meta() engine.Meta {
	return engine.Meta{
		Label:   s.Label(),
		Kind:    engine.KindSandbox,
		Phase:   "run",
		Image:   s.image,
		Network: "bridge",
	}
}

// RunStart creates the container, copies the toby binary in, starts it on its idle
// command, then launches the proxy-only manager via docker exec and returns the host
// side of that exec's stdio gRPC link (the demultiplexed exec stdout as reader, the
// exec stdin as writer). The exec stream carries the gRPC frames, so the container's
// own stdout — and thus `docker logs` — stays empty. The caller serves the Tunnel
// gRPC service over the returned conn and waits for the manager's Ready.
func (s *instance) RunStart(ctx context.Context, spec RunSpec) (net.Conn, error) {
	if code, err := s.resolveImage(ctx, spec); err != nil {
		return nil, err
	} else if code != 0 {
		return nil, exitcode.New(code, "docker image preparation failed")
	}
	ctr, err := s.containers.Start(ctx, s.containerRequest(spec), s.meta())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.runContainer = ctr
	s.mu.Unlock()

	cli, err := s.containers.Client(ctx)
	if err != nil {
		return nil, err
	}
	id := ctr.GetContainerID()

	// Deliver the binary into the created (not yet started) container, then start it
	// on the idle command so the container is live before the manager execs in.
	bin, err := sandboxbinary.SourceBytes()
	if err != nil {
		return nil, err
	}
	if err := s.copyToContainer(ctx, []copyEntry{{
		path: s.TobyBinaryPath(),
		mode: 0o755,
		typ:  tar.TypeReg,
		data: bin,
	}}); err != nil {
		return nil, fmt.Errorf("copy toby binary: %w", err)
	}
	if err := ctr.Start(ctx); err != nil {
		return nil, err
	}

	// Launch the manager as a docker exec; attaching returns its stream from the
	// first byte, so the manager's HTTP/2 client preface is captured, not raced past.
	created, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		User:         "0:0",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{s.TobyBinaryPath(), "sandbox", "manager"},
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox manager exec: %w", err)
	}
	attach, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach sandbox manager exec: %w", err)
	}

	// fd 1 (stdout) carries the gRPC frames; fd 2 (stderr) carries manager logs,
	// which we forward to the host stderr for visibility.
	pr, pw := io.Pipe()
	go func() {
		_, copyErr := dstdcopy.StdCopy(pw, os.Stderr, attach.Reader)
		_ = pw.CloseWithError(copyErr)
	}()

	conn := stdio.NewConn(pr, attach.Conn, func() error {
		attach.Close()
		return nil
	})
	return conn, nil
}

// RunStop tears down the Run container. The container is always stopped; the
// engine's keep-stopped policy decides whether it is removed or left on the host
// for inspection (debug). On context cancellation it still tears down using a
// background context.
func (s *instance) RunStop(ctx context.Context) {
	s.mu.Lock()
	ctr := s.runContainer
	s.runContainer = nil
	s.mu.Unlock()
	if ctr == nil {
		return
	}
	termCtx := ctx
	if ctx.Err() != nil {
		termCtx = context.Background()
	}
	_ = s.containers.Terminate(termCtx, ctr)
}

// RunContainerEnv returns the container's base environment (image defaults plus the
// request env), used to seed the host-held environment map for docker exec.
func (s *instance) RunContainerEnv(ctx context.Context) ([]string, error) {
	cli, err := s.containers.Client(ctx)
	if err != nil {
		return nil, err
	}
	id := s.runContainerID()
	if id == "" {
		return nil, fmt.Errorf("run container is not started")
	}
	info, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if info.Container.Config == nil {
		return nil, nil
	}
	return info.Container.Config.Env, nil
}

func stdinIsTerminal() bool { return isCharDevice(os.Stdin) }

func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
