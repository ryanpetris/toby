package runtime

// containerRequest is the pure, deterministic translation of (spec, phase, class)
// into a testcontainers request. It touches no Docker daemon, so it is
// unit-testable in isolation from the lifecycle in container.go.

import (
	"os"
	"strconv"

	"petris.dev/toby/container/engine"

	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
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
func (s *instance) containerRequest(spec RunSpec, phase phaseKind, class engine.DaemonClass) testcontainers.GenericContainerRequest {
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
	useHostNetwork := needsNetwork && class == engine.DaemonLocalUnix
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

	if needsNetwork && class == engine.DaemonRemote {
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
