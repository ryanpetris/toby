package runtime

// containerRequest is the pure, deterministic translation of (spec, class) into a
// testcontainers request for the single long-lived container. It touches no Docker
// daemon, so it is unit-testable in isolation from the lifecycle in container.go.
//
// The container's main process is an idle command (`toby sandbox idle`) that only
// blocks until teardown, so `docker logs` stays empty. The proxy-only manager and
// the user's tools both run via docker exec with their own stdio; the manager exec's
// stdout is what carries the gRPC link. Each volume is mounted at both its final
// target (for the running tool, with binds layered) and its isolated setup path (so
// the host can chown it without touching those binds). The container is created but
// not started, so the caller can docker cp the binary in before starting it.

import (
	"os"
	"strconv"

	"petris.dev/toby/platform/environ"

	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
)

func (s *instance) containerRequest(spec RunSpec) testcontainers.GenericContainerRequest {
	req := testcontainers.ContainerRequest{Image: s.image}
	req.Cmd = []string{"sandbox", "idle"}
	req.Env = runEnv(spec.Env)
	workdir := s.ChdirDir()
	binary := s.TobyBinaryPath()

	// The main process only idles, so it needs no stdin and no TTY; the manager and
	// tool execs attach their own stdio.
	cfgFns := []func(*dcontainer.Config){func(c *dcontainer.Config) {
		c.User = "0:0"
		c.Entrypoint = []string{binary}
		c.WorkingDir = workdir
		c.Tty = false
	}}

	// Mount each volume at both its final target and its isolated setup path.
	mounts := append([]dmount.Mount{}, s.finalMounts(spec.Binds, spec.Mounts)...)
	mounts = append(mounts, s.setupMounts(spec.Mounts)...)

	req.ConfigModifier = func(c *dcontainer.Config) {
		for _, fn := range cfgFns {
			fn(c)
		}
	}

	var groups []string
	if raw, err := os.Getgroups(); err == nil {
		for _, g := range raw {
			groups = append(groups, strconv.Itoa(g))
		}
	}
	enabled := true
	req.HostConfigModifier = func(h *dcontainer.HostConfig) {
		h.Mounts = mounts
		h.Init = &enabled
		if len(groups) > 0 {
			h.GroupAdd = append(h.GroupAdd, groups...)
		}
		if len(s.portBindings) > 0 {
			h.PortBindings = s.portBindings
		}
		// Bridge networking (the default): the manager binds 127.0.0.1 on the
		// container's own loopback, so the proxy listener stays container-private.
		// Host networking would bind the host's loopback instead. Published ports
		// (h.PortBindings) reach the host only if the in-sandbox service binds
		// 0.0.0.0 rather than just the container's loopback.
	}

	req.Labels = map[string]string{
		"toby.sandbox": s.Label(),
		"toby.phase":   "run",
	}

	// Publish ports through testcontainers' own ExposedPorts field: it rebuilds the
	// exposed-port set from here and drops any HostConfig.PortBindings whose port is
	// not in it, so setting only the moby Config.ExposedPorts is not enough.
	if len(s.exposedPorts) > 0 {
		req.ExposedPorts = exposedPortSpecs(s.exposedPorts)
	}

	// Created but not started: the caller copies the binary in, then starts it.
	return testcontainers.GenericContainerRequest{ContainerRequest: req, Started: false}
}

// runEnv builds the container's base environment: HOME and TERM for the tools that
// later exec into it, and the marker that exposes the hidden `sandbox` command
// tree. The proxy listener address is a fixed constant (tunnel.ProxyAddr), so it
// does not need to be passed in.
func runEnv(env environ.Environment) map[string]string {
	result := map[string]string{
		"TOBY_SANDBOX": "1",
	}
	if home, ok := env["HOME"]; ok {
		result["HOME"] = home
	}
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		result["TERM"] = term
	}
	return result
}
