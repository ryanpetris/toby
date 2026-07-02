// The seam between the race-safe registry and the heavy container work. Lifecycle
// does the once-per-container bring-up and teardown; Handle is the resulting live
// project the session layer drives. The registry calls BringUp outside its lock so
// concurrent Acquires for one key funnel through the entry's ready channel, and
// TearDown when a project goes idle or is stopped.

package project

import "context"

// Handle is a live project's runtime. It is opaque to the registry except for the
// bits daemon.status reports; the session layer type-asserts it to the concrete
// runtime it needs.
type Handle interface {
	// ContainerID is the started container's id (for status and the client's exec).
	ContainerID() string
}

// Request carries the first session's launch inputs to BringUp. It is opaque to the
// registry (which only routes it) and type-asserted by the concrete Lifecycle. Only
// the first Acquire for a key uses it — a project's configuration is frozen at the
// launch that created its container.
type Request any

// Lifecycle performs the expensive per-container bring-up and teardown.
type Lifecycle interface {
	// BringUp creates the container, establishes the tunnel, and runs the once-per
	// container setup, returning a Handle. It is called at most once per live entry.
	BringUp(ctx context.Context, key Key, req Request) (Handle, error)
	// TearDown stops and removes the container behind the handle.
	TearDown(handle Handle)
}
