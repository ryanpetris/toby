// The per-project entry and its lifecycle state. All state transitions happen under
// the registry mutex; the ready/done channels let waiters join an in-flight bring-up
// or teardown instead of racing it.

package project

import "time"

type state int

const (
	// stateStarting: an EnsureUp is running BringUp; waiters block on ready.
	stateStarting state = iota
	// stateReady: the container is up and accepting sessions.
	stateReady
	// stateDraining: teardown is running; waiters block on done, then retry fresh.
	stateDraining
	// stateGone: torn down and removed from the map.
	stateGone
)

// entry is one project's registry slot.
type entry struct {
	key   Key
	state state

	handle Handle
	err    error

	sessions  int
	idleTimer *time.Timer

	// ready is closed when BringUp finishes (success or failure); done is created at
	// the start of teardown and closed when the entry reaches stateGone.
	ready chan struct{}
	done  chan struct{}
}
