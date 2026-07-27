package caddy

// Carries the current acquisition's bounded progress reporter through the
// shared resource registry into a newly started Caddy generation.

import (
	"context"

	"petris.dev/toby/internal/agent/progressio"
	"petris.dev/toby/internal/providergateway"
)

type progressContextKey struct{}

func withProgress(
	ctx context.Context,
	reporter providergateway.ProgressReporter,
) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

func progressFrom(
	ctx context.Context,
) providergateway.ProgressReporter {
	reporter, _ := ctx.Value(progressContextKey{}).(providergateway.ProgressReporter)
	return reporter
}

func startProgress(
	reporter providergateway.ProgressReporter,
	label string,
) *progressio.Operation {
	return progressio.Start(reporter, "caddy", label)
}
