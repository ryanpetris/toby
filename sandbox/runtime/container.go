package runtime

// The Docker-backed Instance: it drives the prime, setup, and run container
// phases through the shared engine.Service (Docker Engine API, via testcontainers
// for creation and the moby client for wait/attach). Building containers shells
// out to the docker CLI (see build.go); everything else goes through the SDK.

import (
	"context"
	"os"
	"sync"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/tools"

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
	primed       bool
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
			"prime":    s.phaseContainerName("prime", true),
			"setup":    s.phaseContainerName("setup", true),
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

func (s *instance) meta(phase phaseKind, class engine.DaemonClass) engine.Meta {
	return engine.Meta{
		Label:   s.Label(),
		Kind:    engine.KindSandbox,
		Phase:   phase.String(),
		Image:   s.image,
		Network: networkLabel(class, phase),
	}
}

// Prime seeds the named volumes from image content (see containerRequest).
func (s *instance) Prime(ctx context.Context, spec RunSpec) (int, error) {
	s.mu.Lock()
	primed := s.primed
	s.mu.Unlock()
	if primed {
		return 0, nil
	}
	if code, err := s.resolveImage(ctx, spec); err != nil || code != 0 {
		return code, err
	}
	class, err := s.containers.DaemonClass(ctx)
	if err != nil {
		return 1, err
	}
	ctr, err := s.containers.Start(ctx, s.containerRequest(spec, phasePrime, class), s.meta(phasePrime, class))
	if err != nil {
		return 1, err
	}
	code, waitErr := s.waitExit(ctx, ctr)
	s.finishPhase(ctx, ctr, spec.Debug)
	if waitErr != nil {
		return code, waitErr
	}
	if code != 0 {
		return code, exitcode.New(code, "docker volume preparation failed")
	}
	s.mu.Lock()
	s.primed = true
	s.mu.Unlock()
	return 0, nil
}

// Setup runs the root manager that chowns the provider volumes; the host drives
// mount-init over the control channel and terminates it, letting it exit.
func (s *instance) Setup(ctx context.Context, spec RunSpec) (int, error) {
	class, err := s.containers.DaemonClass(ctx)
	if err != nil {
		return 1, err
	}
	ctr, err := s.containers.Start(ctx, s.containerRequest(spec, phaseSetup, class), s.meta(phaseSetup, class))
	if err != nil {
		return 1, err
	}
	code, waitErr := s.waitExit(ctx, ctr)
	s.finishPhase(ctx, ctr, spec.Debug)
	return code, waitErr
}

// Run starts the long-lived interactive container and attaches the host
// terminal. It blocks until the container exits and returns its exit code,
// matching the executil.Runner contract the session orchestrator relies on.
func (s *instance) Run(ctx context.Context, spec RunSpec) (int, error) {
	if code, err := s.Prime(ctx, spec); err != nil || code != 0 {
		return code, err
	}
	class, err := s.containers.DaemonClass(ctx)
	if err != nil {
		return 1, err
	}
	ctr, err := s.containers.Start(ctx, s.containerRequest(spec, phaseRun, class), s.meta(phaseRun, class))
	if err != nil {
		return 1, err
	}
	s.mu.Lock()
	s.runContainer = ctr
	s.mu.Unlock()
	code, runErr := s.attachAndWait(ctx, ctr)
	s.finishPhase(ctx, ctr, spec.Debug)
	s.mu.Lock()
	s.runContainer = nil
	s.mu.Unlock()
	return code, runErr
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

// finishPhase removes the container (non-debug) or leaves it for inspection
// while dropping it from the tracking registry (debug). On context cancellation
// it still removes the container using a background context.
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
