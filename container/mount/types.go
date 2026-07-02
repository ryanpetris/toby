package mount

// Data types describing a host bind and the shared-home Access classification.

import "errors"

// Access classifies the permission a bind is given inside the container.
type Access string

const (
	AccessRegular  Access = "regular"
	AccessReadOnly Access = "read_only"
	AccessDev      Access = "dev"
)

// Bind is a passthrough host bind. HostPath must be absolute; the caller resolves it.
type Bind struct {
	HostPath string
	Target   string // container-interior path; "~"/"~/" is expanded
	Access   Access
	Optional bool
}

var (
	errEmptyHostPath = errors.New("bind host path must not be empty")
	errBindTarget    = errors.New("bind target must resolve to an absolute container path")
)
