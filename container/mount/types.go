package mount

// Data types describing what a requester asks for and what the service resolves:
// the permission Access, the volume Request and resolved Entry, a host Bind, and
// the per-session Config.

// Access classifies the permission a mount is given inside the container.
type Access string

const (
	AccessRegular  Access = "regular"
	AccessReadOnly Access = "read_only"
	AccessDev      Access = "dev"
)

// Request asks the service to register a persistent volume mount.
type Request struct {
	Key      Key
	Target   string // container-interior path; "~"/"~/" is expanded to the container home
	Access   Access
	Optional bool
	Setup    SetupFunc // optional; nil means default chown
}

// Bind is a passthrough host bind. HostPath must be absolute; the caller resolves it.
type Bind struct {
	HostPath string
	Target   string // container-interior path; "~"/"~/" is expanded
	Access   Access
	Optional bool
}

// Entry is a resolved persistent volume mount.
type Entry struct {
	Key       Key
	Profile   string
	Volume    string
	Target    string
	Access    Access
	Optional  bool
	SetupPath string

	setup SetupFunc
}

// Config configures the service for a single sandbox session.
type Config struct {
	Profile      string            // global default profile (namespace) for all mounts
	SandboxName  string            // names the runtime home volume's purpose
	ToolProfiles map[string]string // per-tool profile overrides, keyed by tool name
}
