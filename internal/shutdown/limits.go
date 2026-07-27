package shutdown

// Defines the process-wide shutdown time budget shared by each owning layer.

import "time"

const (
	// AgentClientGrace is the time the agent allows connected clients to
	// release launch-owned capabilities before their sessions are canceled.
	AgentClientGrace = 20 * time.Second

	// AgentClientMargin keeps the client's advertised deadline inside the
	// agent's actual acknowledgement deadline.
	AgentClientMargin = 3 * time.Second

	// AgentTransportGrace bounds gRPC stream and RPC drainage after clients
	// have acknowledged shutdown.
	AgentTransportGrace = 5 * time.Second

	// AgentResourceGrace bounds agent-owned resource teardown after the
	// agent transport stops.
	AgentResourceGrace = 10 * time.Second

	// ProcessFinalizationGrace bounds process-wide Fx finalization.
	ProcessFinalizationGrace = 5 * time.Second

	// ClientShutdownGrace bounds a launch client's cleanup when an operating
	// system signal initiates shutdown.
	ClientShutdownGrace = 20 * time.Second

	// ClientResourceReleaseGrace bounds client resource release when there is
	// no shorter agent-provided deadline.
	ClientResourceReleaseGrace = 15 * time.Second

	// SandboxTerminationGrace allows a sandbox process group to respond to
	// SIGTERM before Toby sends SIGKILL.
	SandboxTerminationGrace = 2 * time.Second

	// SandboxReapGrace bounds the wait for a SIGKILLed sandbox process group.
	SandboxReapGrace = 2 * time.Second

	// ResourceStopGrace allows an agent resource process to stop normally.
	ResourceStopGrace = 5 * time.Second

	// ResourceKillGrace bounds cleanup after an agent resource is killed.
	ResourceKillGrace = 2 * time.Second
)

// ClientSandboxReserve leaves enough of a client deadline to escalate from the
// initial interrupt, terminate and reap an unresponsive foreground sandbox,
// and absorb local scheduling delay before client cleanup expires.
const ClientSandboxReserve = SandboxTerminationGrace +
	SandboxReapGrace +
	time.Second
