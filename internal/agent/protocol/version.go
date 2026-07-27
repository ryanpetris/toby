package protocol

// Defines the agent protocol version and message-size limit.

const (
	// Version is the agent application-protocol version understood by this
	// binary.
	Version uint32 = 1

	// MaxMessageBytes bounds every encoded gRPC agent message.
	MaxMessageBytes = 1 << 20

	// MaxStreamDataBytes bounds one data message within an MCP or models byte
	// stream. Larger net.Conn writes are split without changing their byte
	// sequence.
	MaxStreamDataBytes = 32 << 10
)

// SupportsVersion reports whether this client can select the advertised agent
// protocol version.
func SupportsVersion(version uint32) bool {
	for _, supported := range SupportedVersions() {
		if version == supported {
			return true
		}
	}

	return false
}

// SupportedVersions returns the protocol versions implemented by this binary.
func SupportedVersions() []uint32 {
	return []uint32{Version}
}
