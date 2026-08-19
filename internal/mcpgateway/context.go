package mcpgateway

// Merges two cancellation sources for one MCP connector session.

import "context"

// MergeContexts cancels when either parent is done. The returned cancel
// function stops the waiter on the second parent.
func MergeContexts(
	first context.Context,
	second context.Context,
) (context.Context, context.CancelFunc) {
	if first == nil {
		first = context.Background()
	}
	if second == nil {
		second = context.Background()
	}

	ctx, cancel := context.WithCancel(first)
	stop := context.AfterFunc(second, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
