package docker

import (
	"context"
	"os"
	"strconv"

	"petris.dev/toby/container/manager"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/sandbox"

	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

type phaseKind int

const (
	phasePrime phaseKind = iota
	phaseSetup
	phaseRun
)

func (p phaseKind) String() string {
	switch p {
	case phasePrime:
		return "prime"
	case phaseSetup:
		return "setup"
	case phaseRun:
		return "run"
	default:
		return ""
	}
}

// containerRequest builds the full testcontainers request for a phase. It is
// deterministic given (spec, phase, class) and touches no Docker daemon, so it
// is unit-testable. Everything is driven through ConfigModifier/HostConfigModifier
// because the direct ContainerRequest fields are deprecated.
func (s *instance) containerRequest(spec sandbox.RunSpec, phase phaseKind, class manager.DaemonClass) testcontainers.GenericContainerRequest {
	req := testcontainers.ContainerRequest{Image: s.image}

	cfgFns := []func(*dcontainer.Config){func(c *dcontainer.Config) { c.User = "0:0" }}
	var mounts []dmount.Mount
	needsNetwork := phase == phaseSetup || phase == phaseRun

	switch phase {
	case phasePrime:
		// Mount provider volumes at their final targets as root and exit, so
		// Docker seeds empty named volumes from the image content.
		req.Cmd = []string{"-c", "exit"}
		workdir := s.ChdirDir()
		cfgFns = append(cfgFns, func(c *dcontainer.Config) {
			c.Entrypoint = []string{"/bin/sh"}
			c.WorkingDir = workdir
		})
		mounts = s.finalMounts(spec.Binds, spec.Mounts)
	case phaseSetup:
		req.Cmd = spec.Argv
		req.Env = controlEnv(spec.Env, class)
		cfgFns = append(cfgFns, func(c *dcontainer.Config) {
			c.OpenStdin = true
			c.WorkingDir = "/"
		})
		mounts = s.setupMounts(spec.Mounts)
	case phaseRun:
		tty := stdinIsTerminal() && stdoutIsTerminal()
		req.Cmd = spec.Argv
		req.Env = controlEnv(spec.Env, class)
		workdir := s.ChdirDir()
		cfgFns = append(cfgFns, func(c *dcontainer.Config) {
			c.OpenStdin = true
			c.AttachStdin = true
			c.StdinOnce = false
			c.Tty = tty
			c.WorkingDir = workdir
		})
		mounts = s.finalMounts(spec.Binds, spec.Mounts)
	}

	req.ConfigModifier = func(c *dcontainer.Config) {
		for _, fn := range cfgFns {
			fn(c)
		}
	}

	var groups []string
	if phase == phaseRun {
		if raw, err := os.Getgroups(); err == nil {
			for _, g := range raw {
				groups = append(groups, strconv.Itoa(g))
			}
		}
	}
	useHostNetwork := needsNetwork && class == manager.DaemonLocalUnix
	withInit := phase == phaseSetup || phase == phaseRun
	req.HostConfigModifier = func(h *dcontainer.HostConfig) {
		h.Mounts = mounts
		if withInit {
			enabled := true
			h.Init = &enabled
		}
		if len(groups) > 0 {
			h.GroupAdd = append(h.GroupAdd, groups...)
		}
		if useHostNetwork {
			h.NetworkMode = "host"
		}
	}

	if needsNetwork && class == manager.DaemonRemote {
		if port := controlHostPort(spec.Env); port > 0 {
			req.HostAccessPorts = []int{port}
		}
	}

	if spec.Debug {
		req.Name = s.phaseContainerName(phase.String(), true)
	}
	req.Labels = map[string]string{
		"toby.sandbox": s.Label(),
		"toby.phase":   phase.String(),
	}

	return testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true}
}

// Prime seeds the named volumes from image content (see containerRequest).
func (s *instance) Prime(ctx context.Context, spec sandbox.RunSpec) (int, error) {
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
func (s *instance) Setup(ctx context.Context, spec sandbox.RunSpec) (int, error) {
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
func (s *instance) Run(ctx context.Context, spec sandbox.RunSpec) (int, error) {
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
