package commands

import (
	"io"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/control/sandbox"
	"petris.dev/toby/internal/dirty/cli/session"
	"petris.dev/toby/tools"
)

type Params struct {
	Registry       *tools.Registry
	Paths          config.Paths
	TobyConfig     *tobyconfig.Service
	SandboxManager *sandbox.Runner
	SessionRunner  session.Runner
	Args           []string
	Stdout         io.Writer
	Stderr         io.Writer
}
