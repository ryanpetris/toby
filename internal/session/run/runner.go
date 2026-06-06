package run

// The runner contract: Params is the resolved dependency set one launch needs,
// and Runner lets the app invoke a launch behind a small interface.

import (
	"context"
	"io"

	"petris.dev/toby/config"
	"petris.dev/toby/container/engine"
	contextfiles "petris.dev/toby/context/files"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/control/methods/git"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/status"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

type Params struct {
	Registry       *tools.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Engine         *engine.Service
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	HostManager    *host.Service
	Git            *git.Service
	MCPProxy       *mcpproxy.Service
	MCPServer      *mcpserver.Runner
	TobyConfig     *appconfig.Service
	Status         *status.Service
	Stderr         io.Writer

	Runner *lifecycle.Runner
}

type Runner interface {
	Run(context.Context, *tools.Options, appconfig.LaunchOverrides, []string, []string, string) error
}
