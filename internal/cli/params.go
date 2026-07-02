package cli

// Params: the injected dependencies the command tree is built from (the tool
// registry, the session runner, and the output streams).

import (
	"io"

	"petris.dev/toby/config"
	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/tools"
)

type Params struct {
	Registry      *tools.Registry
	Paths         config.Paths
	TobyConfig    *appconfig.Service
	SessionRunner run.Runner
	Args          []string
	Stdout        io.Writer
	Stderr        io.Writer
}
