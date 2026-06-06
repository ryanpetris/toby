package runtime

// The Docker-backed Instance: it creates the single long-lived container, delivers
// the toby binary with docker cp, starts it running the proxy-only manager, and
// hands back the host side of the stdio gRPC link. Tools, mount-init, and file
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
	dcontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// DefaultImage is the image used when no image or build is configured.
const DefaultImage = "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm"

type instance struct {
	BaseInstance
	containers    *engine.Service
	image         string
	build         tools.Build
	containerName string

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
	if debug && s.containerName != "" {
		info["container"] = map[string]any{
			"baseName": s.containerName,
			"run":      s.phaseContainerName("run", true),
		}
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

func (s *instance) phaseContainerName(phase string, debug bool) string {
	if !debug || phase == "" {
		return s.containerName
	}
	return s.containerName + "-" + phase
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

// RunStart creates the container, copies the toby binary in, starts it running the
// proxy-only manager, and returns the host side of the stdio gRPC link (the
// demultiplexed stdout as reader, the attach stdin as writer). The caller serves
// the Tunnel gRPC service over the returned conn and waits for the manager's Ready.
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

	// Deliver the binary into the created (not yet started) container, then start it.
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

	// Attach BEFORE starting so the manager's first gRPC bytes (the HTTP/2 client
	// preface) are captured rather than raced past.
	attach, err := cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, err
	}

	// fd 1 (stdout) carries the gRPC frames; fd 2 (stderr) carries manager logs,
	// which we forward to the host stderr for visibility.
	pr, pw := io.Pipe()
	go func() {
		_, copyErr := dstdcopy.StdCopy(pw, os.Stderr, attach.Reader)
		_ = pw.CloseWithError(copyErr)
	}()

	if err := ctr.Start(ctx); err != nil {
		attach.Close()
		return nil, err
	}

	conn := stdio.NewConn(pr, attach.Conn, func() error {
		attach.Close()
		return nil
	})
	return conn, nil
}

// RunStop tears down the Run container (removing it unless debug), matching the
// finishPhase semantics the prior lifecycle used.
func (s *instance) RunStop(ctx context.Context, debug bool) {
	s.mu.Lock()
	ctr := s.runContainer
	s.runContainer = nil
	s.mu.Unlock()
	s.finishPhase(ctx, ctr, debug)
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

// waitExit blocks until the container leaves the running state and returns its
// exit code. ctx cancellation yields 130, matching ProcessRunner.
func (s *instance) waitExit(ctx context.Context, ctr testcontainers.Container) (int, error) {
	cli, err := s.containers.Client(ctx)
	if err != nil {
		return 1, err
	}
	result := cli.ContainerWait(ctx, ctr.GetContainerID(), client.ContainerWaitOptions{Condition: dcontainer.WaitConditionNotRunning})
	select {
	case res := <-result.Result:
		return int(res.StatusCode), nil
	case werr := <-result.Error:
		if st, serr := ctr.State(ctx); serr == nil && st != nil && !st.Running {
			return st.ExitCode, nil
		}
		return 1, werr
	case <-ctx.Done():
		return 130, ctx.Err()
	}
}

// finishPhase removes the container (non-debug) or leaves it for inspection while
// dropping it from the tracking registry (debug). On context cancellation it still
// removes the container using a background context.
func (s *instance) finishPhase(ctx context.Context, ctr testcontainers.Container, debug bool) {
	if ctr == nil {
		return
	}
	if debug {
		s.containers.Forget(ctr)
		return
	}
	termCtx := ctx
	if ctx.Err() != nil {
		termCtx = context.Background()
	}
	_ = s.containers.Terminate(termCtx, ctr)
}

func stdinIsTerminal() bool { return isCharDevice(os.Stdin) }

func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
