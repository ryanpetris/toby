package cli

// Params: the injected dependencies the command tree is built from (the tool
// registry, the session runner, and the standard streams).

import (
	"io"

	"petris.dev/toby/internal/agent"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/tools"
)

// Params contains dependencies and streams used to build the root command.
type Params struct {
	Registry      *tools.Registry
	Paths         config.Paths
	TobyConfig    *appconfig.Service
	Agent         *agent.Client
	Diagnostics   *diagnostic.Service
	Status        *status.Service
	Warnings      *warning.Service
	SessionRunner run.Runner
	Args          []string
	Stdin         io.ReadCloser
	Stdout        io.Writer
	Stderr        io.Writer
}
