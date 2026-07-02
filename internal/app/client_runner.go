// The client-backed session runner: the launch CLI builds a SessionStartParams from
// the resolved options/overrides and hands it to the daemon client, which runs the
// foreground tool itself. This replaces the in-process one-shot runner — every launch
// now goes through the daemon.

package app

import (
	"context"
	"encoding/json"
	"os"

	"golang.org/x/term"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/client"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/daemon/protocol"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

type clientRunnerParams struct {
	fx.In

	Client *client.Service
}

type clientSessionRunner struct {
	client *client.Service
}

var _ run.Runner = (*clientSessionRunner)(nil)

func newClientSessionRunner(params clientRunnerParams) run.Runner {
	return &clientSessionRunner{client: params.Client}
}

func (r *clientSessionRunner) Run(ctx context.Context, opts *tools.Options, overrides appconfig.LaunchOverrides, extra, requestedTools []string, primary string) error {
	if opts == nil {
		opts = &tools.Options{}
	}
	optionsJSON, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return err
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	cols, rows := 0, 0
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		cols, rows = w, h
	}

	params := protocol.SessionStartParams{
		Options:        optionsJSON,
		Overrides:      overridesJSON,
		Extra:          extra,
		RequestedTools: requestedTools,
		Primary:        primary,
		Install:        opts.Install,
		Upgrade:        opts.Upgrade,
		Interactive:    interactive,
		Cols:           cols,
		Rows:           rows,
	}

	code, err := r.client.StartSession(ctx, params)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitcode.Code(code)
	}
	return nil
}
