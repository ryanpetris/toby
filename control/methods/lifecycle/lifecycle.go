// Package lifecycle holds the control method names for the session handshake that
// spans both sides of the channel: context.init (sandbox announces readiness to the
// host) and sandbox.terminate (host asks the sandbox to shut down). They have no
// single owning capability, so they live in their own package rather than in the
// host or sandbox manager (which would import each other).
package lifecycle

// Control method names for the session lifecycle handshake.
const (
	MethodContextInit      = "context.init"
	MethodSandboxTerminate = "sandbox.terminate"
)
