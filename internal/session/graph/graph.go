// Package graph builds the per-project fx graph: the host services, the selected
// tools, the sandbox runtime, the lifecycle runner, the provider registry, and the
// session-config resolver. It is shared by the one-shot launch runner and the daemon,
// which supply the paths, the effective config, and a populate target for run.Params.
package graph

import (
	"io"

	"petris.dev/toby/config"
	"petris.dev/toby/config/session"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/approval"
	appconfig "petris.dev/toby/internal/config/app"
	contextinit "petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	gitservice "petris.dev/toby/internal/control/mcpserver/services/git"
	sessionservice "petris.dev/toby/internal/control/mcpserver/services/session"
	"petris.dev/toby/internal/control/methods/git"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/session/resolve"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/platform/executil"
	"petris.dev/toby/providers"
	"petris.dev/toby/providers/anthropic"
	"petris.dev/toby/providers/openai"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

// Modules is the per-project fx graph. The caller appends fx.Supply(paths, config) and
// an fx.Populate(&run.Params) target.
func Modules(toolModule fx.Option, stderr io.Writer) []fx.Option {
	return []fx.Option{
		host.Module(),
		approval.Module(),
		engine.Module(),
		mount.Module(),
		mcpproxy.Module(),
		mcpserver.Module(),
		gitservice.Module(),
		sessionservice.Module(),
		sandbox.Module(),
		status.Module(),
		toolModule,
		tools.Module(),
		lifecycle.Module(),
		providers.Module(),
		openai.Module(),
		anthropic.Module(),
		fx.Provide(
			executil.NewProcessRunner,
			contextfiles.NewService,
			contextinit.NewLifecycleHooks,
			sessionconfig.NewHolder,
			resolve.NewLifecycleHooks,
			NewParams(stderr),
		),
	}
}

// paramsIn collects the per-project services run.Params needs.
type paramsIn struct {
	fx.In

	Registry       *tools.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Engine         *engine.Service
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *host.Service
	Git            *git.Service
	Approval       *approval.Service
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *appconfig.Service
	Status         *status.Service
	Runner         *lifecycle.Runner
}

// NewParams builds the run.Params constructor bound to the given stderr.
func NewParams(stderr io.Writer) func(paramsIn) run.Params {
	return func(in paramsIn) run.Params {
		return run.Params{
			Registry:       in.Registry,
			SandboxFactory: in.SandboxFactory,
			SandboxService: in.SandboxService,
			Engine:         in.Engine,
			Paths:          in.Paths,
			ContextFiles:   in.ContextFiles,
			HostManager:    in.HostManager,
			Git:            in.Git,
			Approval:       in.Approval,
			MCPProxy:       in.MCPProxy,
			MCPServer:      in.MCPServer,
			TobyConfig:     in.TobyConfig,
			Status:         in.Status,
			Stderr:         stderr,
			Runner:         in.Runner,
		}
	}
}
