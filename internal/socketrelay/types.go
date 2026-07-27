package socketrelay

// Defines relay registrations and the optional concrete-tool contribution
// contract.

// Request grants one sandbox socket access to a host Unix socket through the
// launch process.
type Request struct {
	HostSocket    string
	SandboxSocket string
}

// Contributor is implemented by concrete built-in tools that need a
// run-scoped relay to a host Unix socket.
type Contributor interface {
	// SocketRelays returns the run-scoped socket relays contributed by the tool.
	SocketRelays() ([]Request, error)
}
