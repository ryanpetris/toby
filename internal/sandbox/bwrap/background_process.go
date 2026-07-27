package bwrap

// Declares the lifecycle contract for one agent-owned, noninteractive
// Bubblewrap process.

import "context"

// BackgroundProcess is an asynchronously running Bubblewrap process. Done
// closes only after Bubblewrap and its retained init and payload identities
// have exited and the direct Bubblewrap child has been reaped. Stop sends
// SIGTERM to the exact payload so it can shut down gracefully. Kill sends
// SIGKILL to the exact payload and outer monitor to force tree teardown.
type BackgroundProcess interface {
	// Done closes after the process tree exits.
	Done() <-chan struct{}
	// Err returns the terminal process error.
	Err() error
	// Stop requests graceful payload termination.
	Stop(context.Context) error
	// Kill forcibly terminates the payload and monitor.
	Kill(context.Context) error
}
