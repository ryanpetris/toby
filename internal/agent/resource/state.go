package resource

// Declares the externally observable resource and lease lifecycle states.

// State is the lifecycle state of one canonical background resource.
type State string

const (
	// StateCold means the resource has not started.
	StateCold State = "cold"
	// StateStarting means a generation is starting.
	StateStarting State = "starting"
	// StateReady means the resource accepts work.
	StateReady State = "ready"
	// StateIdle means the resource has no leases.
	StateIdle State = "idle"
	// StateStopping means the resource is terminating.
	StateStopping State = "stopping"
	// StateFailed means the latest generation failed.
	StateFailed State = "failed"
)

// LeaseState is the lifecycle state of one run's reference to a resource.
type LeaseState string

const (
	// LeaseOpening means acquisition is in progress.
	LeaseOpening LeaseState = "opening"
	// LeaseActive means the lease holds the resource.
	LeaseActive LeaseState = "active"
	// LeaseReleasing means release is in progress.
	LeaseReleasing LeaseState = "releasing"
	// LeaseClosed means the lease no longer holds the resource.
	LeaseClosed LeaseState = "closed"
)
