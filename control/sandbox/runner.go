// Package sandbox is the in-sandbox control endpoint: the Runner builds the
// capability router once per process and starts a Runtime per launch, which dials
// the host, drives the connection lifecycle, and dispatches inbound requests.
package sandbox

import (
	"context"
	"fmt"

	"petris.dev/toby/control"
	commandcap "petris.dev/toby/control/methods/command"
	envcap "petris.dev/toby/control/methods/env"

	"go.uber.org/fx"
)

// Runner is the fx-provided entry point for the hidden `toby sandbox manager`
// command. It builds the capability router once and starts a Runtime per launch.
type Runner struct {
	router  *control.Router
	env     *envcap.Service
	command *commandcap.Service
}

type RunnerParams struct {
	fx.In

	Capabilities []control.Capability `group:"control.sandbox.handlers"`
	Env          *envcap.Service
	Command      *commandcap.Service
}

func NewRunner(params RunnerParams) (*Runner, error) {
	router, err := control.NewRouter(params.Capabilities)
	if err != nil {
		return nil, err
	}
	return &Runner{router: router, env: params.Env, command: params.Command}, nil
}

func (r *Runner) Run(ctx context.Context, controlPath string) error {
	if r == nil || r.router == nil {
		return fmt.Errorf("sandbox manager router is not configured")
	}
	return NewRuntime(r.router, r.env, r.command).Run(ctx, controlPath)
}
