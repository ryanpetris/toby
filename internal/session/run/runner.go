// Package run orchestrates one native Toby launch session.
package run

// The runner contract used by the CLI to invoke one native launch.

import (
	"context"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/tools"
)

// Runner executes one complete sandbox session.
type Runner interface {
	// Run prepares and launches one sandbox session.
	Run(context.Context, *tools.Options, appconfig.LaunchOverrides, []string, []string, string) error
}
