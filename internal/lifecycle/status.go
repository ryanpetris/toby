package lifecycle

// Carries the parent tool operation into lifecycle sandbox commands.

import (
	"context"

	"petris.dev/toby/internal/status"
)

type commandStatusKey struct{}

func withCommandOperation(
	ctx context.Context,
	operation *status.Operation,
) context.Context {
	return context.WithValue(ctx, commandStatusKey{}, operation)
}

// CommandOperation returns the parent tool operation for a lifecycle sandbox
// command.
func CommandOperation(ctx context.Context) *status.Operation {
	operation, _ := ctx.Value(commandStatusKey{}).(*status.Operation)
	return operation
}
