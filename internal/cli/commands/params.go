package commands

import (
	"io"

	"petris.dev/toby/internal/cli/session"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/tools/tool"
)

type Params struct {
	Registry       *tool.Registry
	Paths          config.Paths
	TobyConfig     *tobyconfig.Service
	SandboxManager *sandboxmanager.Runner
	SessionRunner  session.Runner
	Args           []string
	Stdout         io.Writer
	Stderr         io.Writer
}
