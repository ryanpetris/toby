package protocol

// Defines transport-independent agent status values.

// ServiceState is the process-level state reported by agent status.
type ServiceState string

const (
	// ServiceStarting means the agent is initializing.
	ServiceStarting ServiceState = "starting"
	// ServiceReady means the agent accepts requests.
	ServiceReady ServiceState = "ready"
	// ServiceStopping means the agent is shutting down.
	ServiceStopping ServiceState = "stopping"
)

// ServiceStatusResponse reports non-secret process state and activity counts.
type ServiceStatusResponse struct {
	CorrelationID   CorrelationID `json:"-"`
	BinaryVersion   string        `json:"binary_version"`
	State           ServiceState  `json:"state"`
	ActiveSessions  uint64        `json:"active_sessions"`
	ActiveLeases    uint64        `json:"active_leases"`
	ActiveResources uint64        `json:"active_resources"`
	ActiveStreams   uint64        `json:"active_streams"`
}
