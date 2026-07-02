// Package run brings the per-project+profile netns unit up and holds it open across
// launches. It stands up the netns container (published ports + shared network
// namespace), serves the host reverse proxy and Toby MCP server over the proxy tunnel,
// and registers the configured MCP sidecar leases. Each session runs the tool
// lifecycle against the shared home manager, writes a launch descriptor into the home
// volume, and creates a tool container joined to this netns; the daemon attaches to
// and tears that tool container down per session.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	pathpkg "path"
	"time"

	"google.golang.org/grpc"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/control/host"
	insandbox "petris.dev/toby/internal/control/sandbox"
	"petris.dev/toby/internal/control/stdio"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/internal/lifecycle"
	sandboxapi "petris.dev/toby/sandbox"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

const readyTimeout = 30 * time.Second

// BringUpRequest describes the netns unit to bring up.
type BringUpRequest struct {
	Options        *tools.Options
	RequestedTools []string
	Primary        string
	Profile        string
	NetnsName      string
}

// Container is a live netns unit held open for one project+profile.
type Container struct {
	params  Params
	spec    sandbox.Spec
	toolset *tools.Toolset
	lctx    lifecycle.Context
	opts    *tools.Options
	profile string
	binVol  string

	netns      *sandbox.Manager
	tunnelSrv  *tunnel.Server
	grpcSrv    *grpc.Server
	mcpStarted bool
}

// BringUp stands up the netns container, serves the proxy tunnel, registers the Toby
// MCP server + configured sidecars, and prepares the toolset (host phase). It does
// not launch any tool.
func BringUp(ctx context.Context, params Params, req BringUpRequest) (*Container, error) {
	opts := req.Options
	if opts == nil {
		opts = &tools.Options{}
	}
	if err := prepareConfiguredProjects(params.Stderr, params.Paths.Home, opts, params.TobyConfig.Settings().SuppressWarnings); err != nil {
		return nil, err
	}
	params.Status.SetPlain(params.TobyConfig.DebugEnabled())
	if params.Engine == nil {
		return nil, fmt.Errorf("container engine is not configured")
	}
	params.Engine.SetKeepStopped(params.TobyConfig.DebugEnabled())
	params.Status.Set("Connecting to Docker")
	if err := params.Engine.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker socket not reachable (is the daemon running, or DOCKER_HOST set?): %w", err)
	}

	params.Status.Set("Building sandbox")
	spec, err := params.SandboxFactory.Resolve(opts, params.TobyConfig.Image(), params.TobyConfig.Build(), params.TobyConfig.Ports())
	if err != nil {
		return nil, err
	}
	image, err := sandbox.EnsureImage(ctx, spec, params.TobyConfig.DebugEnabled())
	if err != nil {
		return nil, err
	}
	spec.Image = image

	c := &Container{params: params, spec: spec, opts: opts, profile: req.Profile}
	if err := c.bringUp(ctx, req); err != nil {
		c.Close(context.Background())
		return nil, err
	}
	return c, nil
}

func (c *Container) bringUp(ctx context.Context, req BringUpRequest) error {
	params := c.params
	if params.SandboxService == nil {
		return fmt.Errorf("sandbox service is not configured")
	}

	// Ensure the shared read-only toby-binary volume once.
	cli, err := params.Engine.Client(ctx)
	if err != nil {
		return err
	}
	binVol, err := sandbox.EnsureBinVolume(ctx, cli)
	if err != nil {
		return err
	}
	c.binVol = binVol

	params.SandboxService.Configure(c.spec, req.Profile)

	toolset, err := params.Registry.Build(req.RequestedTools, req.Primary)
	if err != nil {
		return err
	}
	c.toolset = toolset
	c.lctx = lifecycle.Context{Options: c.opts, Stderr: params.Stderr, SuppressWarnings: params.TobyConfig.Settings().SuppressWarnings}

	// Host phase: tools declare their host binds (docker socket, ~/.docker), applied to
	// the tool container.
	params.Status.Set("Preparing tools")
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseHostPrepare, toolset, c.lctx, false); err != nil {
		return err
	}

	if params.Git != nil {
		params.Git.SetResolver(params.SandboxService)
	}
	params.SandboxService.SetApprovalPrompter(nil)
	if params.Approval != nil {
		params.Approval.SetPrompterSource(params.SandboxService)
		if params.Git != nil {
			params.Git.SetApprover(params.Approval)
		}
	}

	return c.startNetns(ctx, req)
}

// startNetns creates the netns container, serves the proxy tunnel over its manager
// stdio, and registers the Toby MCP server + configured sidecar proxies.
func (c *Container) startNetns(ctx context.Context, req BringUpRequest) error {
	params := c.params
	if params.HostManager == nil || params.HostManager.HTTPProxy == nil {
		return fmt.Errorf("http proxy service is not configured")
	}

	ports, err := sandbox.NewPortSpec(c.spec.Ports)
	if err != nil {
		return err
	}

	params.Status.Set("Starting network sandbox")
	netns, err := sandbox.StandUpManager(ctx, params.Engine, sandbox.ManagerSpec{
		Name:      req.NetnsName,
		Label:     c.spec.Label,
		Kind:      "netns",
		Image:     c.spec.Image,
		BinVolume: c.binVol,
		Ports:     ports,
	})
	if err != nil {
		return err
	}
	c.netns = netns

	ready := make(chan string, 1)
	c.tunnelSrv = tunnel.NewServer(params.HostManager.HTTPProxy, func(addr string) {
		select {
		case ready <- addr:
		default:
		}
	})
	c.grpcSrv = grpc.NewServer()
	tunnel.RegisterTunnelServer(c.grpcSrv, c.tunnelSrv)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = c.grpcSrv.Serve(stdio.NewListener(netns.Conn()))
	}()

	params.Status.Set("Waiting for network sandbox")
	select {
	case <-ready:
	case <-serveDone:
		return fmt.Errorf("netns manager exited before reporting ready")
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(readyTimeout):
		return fmt.Errorf("timed out waiting for netns manager to start")
	}

	mcpURL, err := registerTobyMCPProxy(params, params.HostManager, c.opts, c.toolset.OrderedToolNames(), req.Primary)
	if err != nil {
		return err
	}
	params.SandboxService.SetTobyMCPURL(mcpURL)
	if params.MCPProxy != nil {
		params.Status.Set("Starting MCP servers")
		if err := params.MCPProxy.Configure(ctx, params.TobyConfig, mcpDefaults(params.TobyConfig)); err != nil {
			return err
		}
		c.mcpStarted = true
	}
	return nil
}

// HomeBinding is the shared home container's control link + identity, acquired by the
// daemon per session and handed to PreLaunch.
type HomeBinding struct {
	Client  *host.SandboxClient
	BaseEnv []string
	UID     int
	GID     int
}

// PreLaunch runs the per-session tool lifecycle against the shared home manager,
// writes the launch descriptor into the home volume, and creates (does not start) the
// tool container joined to this netns. It returns the tool container id and whether
// the managed terminal is enabled. force upgrades installed tools.
func (c *Container) PreLaunch(ctx context.Context, home HomeBinding, sid string, extra []string, force bool, sink sandbox.InstallSink) (string, bool, error) {
	params := c.params
	svc := params.SandboxService
	svc.BindHome(home.Client, home.BaseEnv, home.UID, home.GID)
	svc.SetInstallSink(sink)
	defer svc.SetInstallSink(nil)

	if params.ContextFiles == nil {
		return "", false, fmt.Errorf("context files service is not configured")
	}
	params.ContextFiles.SetSandbox(svc)
	params.ContextFiles.SetOwner(home.UID, home.GID)
	params.ContextFiles.Reset()

	// ConfigureSandbox seeds the host-held environment (e.g. NPM_CONFIG_PREFIX and the
	// PATH the installed tools land on), so it must run before Install.
	params.Status.Set("Configuring sandbox")
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseConfigureSandbox, c.toolset, c.lctx, false); err != nil {
		return "", false, err
	}
	params.Status.Set("Installing tools")
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, c.toolset, c.lctx, force); err != nil {
		return "", false, err
	}
	params.Status.Set("Writing config files")
	if err := renderGeneratedConfig(ctx, params, c.toolset, c.lctx); err != nil {
		return "", false, err
	}
	params.Status.Set("Initializing sandbox")
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInitSandbox, c.toolset, c.lctx, false); err != nil {
		return "", false, err
	}

	primary := c.toolset.Primary()
	if primary == nil {
		return "", false, fmt.Errorf("toolset cannot launch without a primary tool")
	}
	argv, err := primary.LaunchCommand(ctx, extra)
	if err != nil {
		return "", false, err
	}

	toolID, err := c.createTool(ctx, home, sid, argv)
	if err != nil {
		return "", false, err
	}
	return toolID, params.TobyConfig.ManagedTerminalEnabled(), nil
}

// Install configures the sandbox environment and runs the install phase against the
// shared home manager (the `--install` path). force upgrades installed tools.
func (c *Container) Install(ctx context.Context, home HomeBinding, force bool, sink sandbox.InstallSink) error {
	svc := c.params.SandboxService
	svc.BindHome(home.Client, home.BaseEnv, home.UID, home.GID)
	svc.SetInstallSink(sink)
	defer svc.SetInstallSink(nil)

	// Configure must run before Install so the environment (npm prefix/PATH) is set.
	if err := c.params.Runner.RunPhase(ctx, lifecycle.PhaseConfigureSandbox, c.toolset, c.lctx, false); err != nil {
		return err
	}
	return c.params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, c.toolset, c.lctx, force)
}

// runDir is the per-session directory in the shared home holding the launch descriptor.
func runDir(sid string) string { return pathpkg.Join(layout.Run, sid) }

func (c *Container) createTool(ctx context.Context, home HomeBinding, sid string, argv []string) (string, error) {
	svc := c.params.SandboxService
	descriptor := insandbox.LaunchDescriptor{Argv: argv, Env: svc.LaunchEnv(), WorkingDir: svc.ChdirDir()}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	dir := runDir(sid)
	if err := home.Client.FileMkdirOwned(ctx, dir, 0o755, home.UID, home.GID); err != nil {
		return "", err
	}
	descriptorPath := pathpkg.Join(dir, "launch.json")
	if err := home.Client.FileCreateOwned(ctx, descriptorPath, data, 0o644, home.UID, home.GID); err != nil {
		return "", err
	}

	binds := sandbox.ToolBinds(svc.StartBindSnapshot(), svc.ProjectMounts())
	return sandbox.CreateTool(ctx, c.params.Engine, sandbox.ToolSpec{
		Name:           "toby.tool." + sid,
		Label:          c.spec.Label,
		Image:          c.spec.Image,
		BinVolume:      c.binVol,
		HomeVolume:     mount.HomeVolume(c.profile),
		NetnsID:        c.netns.ContainerID(),
		Binds:          binds,
		User:           fmt.Sprintf("%d:%d", home.UID, home.GID),
		DescriptorPath: descriptorPath,
	})
}

// ReleaseSession removes the per-session run directory from the shared home.
func (c *Container) ReleaseSession(ctx context.Context, home *host.SandboxClient, sid string) {
	if home != nil {
		_ = home.FileDelete(ctx, runDir(sid), true)
	}
}

// ContainerID returns the netns container id (the project's tracked container).
func (c *Container) ContainerID() string {
	if c.netns == nil {
		return ""
	}
	return c.netns.ContainerID()
}

// Image is the resolved container image (shared by the home and tool containers).
func (c *Container) Image() string { return c.spec.Image }

// BinVolume is the read-only toby-binary volume mounted into every container.
func (c *Container) BinVolume() string { return c.binVol }

// SetApprovalPrompter routes this project's approval prompts to p (nil clears).
func (c *Container) SetApprovalPrompter(p sandboxapi.ApprovalPrompter) {
	c.params.SandboxService.SetApprovalPrompter(p)
}

// Close tears down the netns unit: the proxy tunnel, MCP leases, and netns container.
func (c *Container) Close(ctx context.Context) {
	if c.grpcSrv != nil {
		c.grpcSrv.Stop()
	}
	if c.tunnelSrv != nil {
		_ = c.tunnelSrv.Close()
	}
	if c.mcpStarted && c.params.MCPProxy != nil {
		c.params.MCPProxy.Close()
	}
	if c.netns != nil {
		c.netns.Close(ctx)
	}
	c.params.Status.Stop()
}
