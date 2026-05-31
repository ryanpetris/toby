package commands

import (
	"io"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools/tool"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
	SandboxService *sandbox.SandboxService
	Paths          config.Paths
	ContextFiles   *contextfiles.Service
	ContextInit    []contextinit.Registration
	HostManager    *hostmanager.HostManager
	SandboxManager *sandboxmanager.Runner
	MCPServer      *mcpserver.Runner
	TobyConfig     *tobyconfig.Service
	Args           []string
	Stdout         io.Writer
	Stderr         io.Writer
}
