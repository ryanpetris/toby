package runtime

// The three container kinds of the profile-home topology, all created via the shared
// engine and the read-only toby-binary volume (no per-container binary docker-cp):
//
//   - Home  (per profile, root): owns the shared home volume; runs the `sandbox home`
//     manager (files + streamed exec). Persistent, deterministic name.
//   - Netns (per project+profile, root): owns published ports + the network namespace;
//     runs the `sandbox netns` manager (proxy only). Persistent, deterministic name.
//   - Tool  (per invocation, invoking user): joins the netns via NetworkMode, mounts
//     the home volume + workspace, and runs `sandbox launch` as its main process.
//     Ephemeral; the client attaches to it.
//
// Home and netns are brought up with StandUpManager (idle main + a manager docker-exec
// whose stdio carries the gRPC link). The tool container is created (not started) by
// CreateTool and started by the client's attach.

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"

	dstdcopy "github.com/moby/moby/api/pkg/stdcopy"
	dcontainer "github.com/moby/moby/api/types/container"
	dmount "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/internal/control/stdio"
)

// binMount is the read-only toby-binary volume mount, shared by every container.
func binMount(binVolume string) dmount.Mount {
	return dmount.Mount{Type: dmount.TypeVolume, Source: binVolume, Target: BinDir, ReadOnly: true}
}

// tobyBin is the path to the toby binary inside every container (the RO bin volume).
func tobyBin() string { return BinDir + "/toby" }

// ManagerSpec describes a persistent home/netns container.
type ManagerSpec struct {
	Name       string   // deterministic container name (create-or-reuse)
	Label      string   // toby.sandbox label (project or profile) for status/sweep
	Kind       string   // "home" or "netns"; also the `sandbox <kind>` manager role
	Image      string   // container image
	BinVolume  string   // the RO toby-binary volume
	HomeVolume string   // the home volume (home container only; "" for netns)
	Ports      PortSpec // published ports (netns container only)
}

// Manager is a live home/netns container plus the stdio link to its manager exec.
type Manager struct {
	engine *engine.Service
	ctr    testcontainers.Container
	conn   net.Conn
	id     string
}

// StandUpManager creates-or-reuses the named container, starts its idle main, execs the
// `sandbox <kind>` manager, and returns a Manager wrapping the stdio gRPC link.
func StandUpManager(ctx context.Context, eng *engine.Service, spec ManagerSpec) (*Manager, error) {
	req := managerRequest(spec)
	ctr, err := eng.Start(ctx, req, engine.Meta{Label: spec.Label, Kind: engine.KindSandbox, Phase: spec.Kind, Image: spec.Image, Network: "bridge"})
	if err != nil {
		return nil, err
	}
	if err := ctr.Start(ctx); err != nil {
		_ = eng.Terminate(ctx, ctr)
		return nil, err
	}

	conn, err := managerExec(ctx, eng, ctr.GetContainerID(), []string{tobyBin(), "sandbox", spec.Kind})
	if err != nil {
		_ = eng.Terminate(ctx, ctr)
		return nil, err
	}
	return &Manager{engine: eng, ctr: ctr, conn: conn, id: ctr.GetContainerID()}, nil
}

func (m *Manager) ContainerID() string { return m.id }
func (m *Manager) Conn() net.Conn      { return m.conn }

// Close stops the container and closes the stdio link.
func (m *Manager) Close(ctx context.Context) {
	if m.conn != nil {
		_ = m.conn.Close()
	}
	if m.ctr != nil {
		_ = m.engine.Terminate(ctx, m.ctr)
	}
}

// managerRequest builds the create-or-reuse request for a home/netns container: root
// user, idle main, the RO bin volume, plus the home volume (home) or ports (netns).
func managerRequest(spec ManagerSpec) testcontainers.GenericContainerRequest {
	req := testcontainers.ContainerRequest{Image: spec.Image, Name: spec.Name}
	req.Cmd = []string{"sandbox", "idle"}
	req.Env = map[string]string{"TOBY_SANDBOX": "1", "HOME": layout.Home}
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		req.Env["TERM"] = term
	}

	mounts := []dmount.Mount{binMount(spec.BinVolume)}
	if spec.HomeVolume != "" {
		mounts = append(mounts, dmount.Mount{Type: dmount.TypeVolume, Source: spec.HomeVolume, Target: layout.Home})
	}

	req.ConfigModifier = func(c *dcontainer.Config) {
		c.User = "0:0"
		c.Entrypoint = []string{tobyBin()}
		c.Tty = false
	}
	enabled := true
	req.HostConfigModifier = func(h *dcontainer.HostConfig) {
		h.Mounts = mounts
		h.Init = &enabled
		if len(spec.Ports.bindings) > 0 {
			h.PortBindings = spec.Ports.bindings
		}
	}
	req.Labels = map[string]string{"toby.sandbox": spec.Label, "toby.phase": spec.Kind}
	if len(spec.Ports.exposed) > 0 {
		req.ExposedPorts = spec.Ports.exposedSpecs()
	}
	return testcontainers.GenericContainerRequest{ContainerRequest: req, Reuse: true, Started: false}
}

// managerExec starts the manager subcommand as a docker exec (root) and wraps its
// stdio: fd1 carries the gRPC frames, fd2 the manager logs (to the daemon's stderr).
func managerExec(ctx context.Context, eng *engine.Service, id string, argv []string) (net.Conn, error) {
	cli, err := eng.Client(ctx)
	if err != nil {
		return nil, err
	}
	created, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		User:         "0:0",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          argv,
	})
	if err != nil {
		return nil, err
	}
	attach, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		_, _ = dstdcopy.StdCopy(pw, os.Stderr, attach.Reader)
		_ = pw.Close()
	}()
	return stdio.NewConn(pr, attach.Conn, func() error { attach.Close(); return nil }), nil
}

// ToolSpec describes an ephemeral per-invocation tool container.
type ToolSpec struct {
	Name           string         // toby.tool.<sessionID>
	Label          string         // toby.sandbox label (project) for the cascade
	Image          string         // container image
	BinVolume      string         // RO toby-binary volume
	HomeVolume     string         // the profile's home volume
	NetnsID        string         // container id whose network namespace to join
	Binds          []dmount.Mount // workspace + docker binds
	User           string         // "uid:gid"
	DescriptorPath string         // path (in the home volume) to the launch descriptor
	Env            map[string]string
}

// CreateTool creates (but does not start) the tool container. The client attaches to
// and starts it; its main process execs the tool from the launch descriptor.
func CreateTool(ctx context.Context, eng *engine.Service, spec ToolSpec) (string, error) {
	mounts := append([]dmount.Mount{
		binMount(spec.BinVolume),
		{Type: dmount.TypeVolume, Source: spec.HomeVolume, Target: layout.Home},
	}, spec.Binds...)

	env := map[string]string{"TOBY_SANDBOX": "1", "HOME": layout.Home}
	for k, v := range spec.Env {
		env[k] = v
	}
	if term, ok := os.LookupEnv("TERM"); ok && term != "" {
		env["TERM"] = term
	}

	req := testcontainers.ContainerRequest{Image: spec.Image, Name: spec.Name}
	req.Env = env
	req.ConfigModifier = func(c *dcontainer.Config) {
		c.User = spec.User
		c.Entrypoint = []string{tobyBin()}
		c.OpenStdin = true
		c.AttachStdin = true
		c.Tty = true
	}
	req.Cmd = []string{"sandbox", "launch", spec.DescriptorPath}
	enabled := true
	var groups []string
	if raw, err := os.Getgroups(); err == nil {
		for _, g := range raw {
			groups = append(groups, strconv.Itoa(g))
		}
	}
	req.HostConfigModifier = func(h *dcontainer.HostConfig) {
		h.Mounts = mounts
		h.Init = &enabled
		h.NetworkMode = dcontainer.NetworkMode("container:" + spec.NetnsID)
		if len(groups) > 0 {
			h.GroupAdd = append(h.GroupAdd, groups...)
		}
	}
	req.Labels = map[string]string{"toby.sandbox": spec.Label, "toby.phase": "tool", "toby.tool": spec.Name}

	ctr, err := eng.Start(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: false}, engine.Meta{Label: spec.Label, Kind: engine.KindSandbox, Phase: "tool", Image: spec.Image})
	if err != nil {
		return "", err
	}
	return ctr.GetContainerID(), nil
}
