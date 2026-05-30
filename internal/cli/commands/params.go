package commands

import (
	"io"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/contextinit"
	"petris.dev/toby/internal/hostmanager"
	"petris.dev/toby/internal/mcpserver"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandboxmanager"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
)

type Params struct {
	Registry       *tool.Registry
	SandboxFactory sandbox.Factory
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
