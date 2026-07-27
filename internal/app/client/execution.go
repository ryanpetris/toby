package client

// Constructs the statically composed Linux session runner without creating a
// nested Fx application for each launch.

import (
	"os"

	"petris.dev/toby/internal/session/run"
)

func newSessionRunner(params sessionRunnerParams) run.Runner {
	return run.NewNativeRunner(run.NativeRunnerParams{
		Paths:         params.Paths,
		Registry:      params.Registry,
		BaseConfig:    params.BaseConfig,
		LaunchConfig:  params.LaunchConfig,
		SessionConfig: params.SessionConfig,
		Agent:         params.Agent,
		Diagnostics:   params.Diagnostics,
		Lifecycle:     params.Lifecycle,
		Sandbox:       params.Sandbox,
		Git:           params.Git,
		Approval:      params.Approval,
		Status:        params.Status,
		Warnings:      params.Warnings,
		Shutdown:      params.Shutdown,
		Stdin:         os.Stdin,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	})
}
