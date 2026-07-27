package mount

// Data contracts for managed tool volumes, external binds, seeding, and
// sandbox access.

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Access controls how a mount is exposed inside a sandbox.
type Access string

const (
	// AccessRegular provides the sandbox its normal writable mount.
	AccessRegular Access = "regular"
	// AccessReadOnly exposes the mount without write access.
	AccessReadOnly Access = "read_only"
	// AccessDev provides the development mount behavior.
	AccessDev Access = "dev"
)

// Validate verifies that the access mode is supported.
func (a Access) Validate() error {
	switch a {
	case AccessRegular, AccessReadOnly, AccessDev:
		return nil
	default:
		return fmt.Errorf("invalid mount access %q", a)
	}
}

// Seed describes optional first-use content copied from the immutable image.
type Seed struct {
	ImagePath string
}

// Validate verifies that the seed image path is usable.
func (s Seed) Validate() error {
	if s.ImagePath == "" {
		return nil
	}
	return validateSandboxPath("seed image path", s.ImagePath)
}

// Request is a tool's unresolved request for a persistent volume.
type Request struct {
	Key      Key
	Target   string
	Access   Access
	Optional bool
	Seed     Seed
}

// Entry is a tool volume resolved to a verified native host path.
type Entry struct {
	Key      Key
	Profile  string
	HostPath string
	Target   string
	Access   Access
	Optional bool
	Seed     Seed
}

// Validate verifies a managed mount entry.
func (e Entry) Validate() error {
	if err := e.Key.Validate(); err != nil {
		return err
	}
	if e.Profile == "" {
		return fmt.Errorf("tool-volume profile must not be empty")
	}
	if err := validateHostPath("tool-volume host path", e.HostPath); err != nil {
		return err
	}
	if err := validateSandboxPath("tool-volume target", e.Target); err != nil {
		return err
	}
	if err := e.Access.Validate(); err != nil {
		return err
	}
	if err := e.Seed.Validate(); err != nil {
		return err
	}
	return nil
}

// Bind exposes an existing host path without making Toby its storage owner.
type Bind struct {
	HostPath string
	Target   string
	Access   Access
	Optional bool
}

// Validate verifies a host bind declaration.
func (b Bind) Validate() error {
	if err := validateHostPath("bind host path", b.HostPath); err != nil {
		return err
	}
	if err := validateSandboxPath("bind target", b.Target); err != nil {
		return err
	}
	return b.Access.Validate()
}

// ValidateTarget verifies one absolute, clean POSIX sandbox path.
func ValidateTarget(value string) error {
	return validateSandboxPath("sandbox target", value)
}

// TargetsOverlap reports whether either absolute sandbox path contains the
// other. Equal targets overlap.
func TargetsOverlap(a, b string) bool {
	a = path.Clean(a)
	b = path.Clean(b)
	if a == "/" || b == "/" {
		return true
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func validateSandboxPath(label, value string) error {
	if value == "" || !path.IsAbs(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be an absolute POSIX path: %q", label, value)
	}
	if cleaned := path.Clean(value); cleaned != value {
		return fmt.Errorf("%s must be clean: %q", label, value)
	}
	return nil
}

func validateHostPath(label, value string) error {
	if value == "" || !filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be an absolute host path: %q", label, value)
	}
	if cleaned := filepath.Clean(value); cleaned != value {
		return fmt.Errorf("%s must be clean: %q", label, value)
	}
	return nil
}
