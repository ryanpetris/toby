package resource

// Defines the boundary between generic lifecycle coordination and concrete
// agent-owned background processes.

import "context"

// Factory starts one fully ready generation. Start must not return a successful
// Instance until both process startup and protocol readiness have completed.
// Registry owns the context and cancels it when startup times out, its last
// opening lease is released, or the agent shuts down. Start must promptly
// honor that cancellation and reap any partial children before returning. A
// failed Start should return a nil Instance; Registry defensively terminates a
// non-nil failure result before allowing a replacement.
type Factory interface {
	// Start creates and starts one resource generation.
	Start(context.Context, Key, uint64) (Instance, error)
}

// Instance supervises one ready resource generation. Done closes only after the
// generation has exited and been reaped. Err is safe to call after Done closes.
// Stop requests graceful termination; Kill performs forced termination. Both
// methods must honor their context deadline.
type Instance interface {
	// Done closes when the instance exits.
	Done() <-chan struct{}
	// Err returns the instance's terminal error.
	Err() error
	// Stop requests graceful termination.
	Stop(context.Context) error
	// Kill forces termination.
	Kill(context.Context) error
}
