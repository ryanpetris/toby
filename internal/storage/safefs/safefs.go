// Package safefs provides Linux directory capabilities for descriptor-relative
// filesystem operations beneath opened per-user roots.
package safefs

import (
	"errors"
	"io"
	"os"

	"petris.dev/toby/internal/diagnostic"
)

var (
	// ErrUnsupported reports that the requested filesystem guarantee is not
	// available on the current platform or filesystem.
	ErrUnsupported = errors.New("safe filesystem operation is unsupported")

	// ErrUnsafePath reports a path, type, or identity transition that violates
	// the package's containment requirements.
	ErrUnsafePath = errors.New("unsafe filesystem path")

	// ErrLimitExceeded reports that a caller-supplied traversal or read bound was
	// exhausted.
	ErrLimitExceeded = errors.New("safe filesystem limit exceeded")

	// ErrWouldBlock reports that a non-blocking lock is already held.
	ErrWouldBlock = errors.New("filesystem lock would block")
)

// Directory retains an opened directory capability. Operations resolve names
// relative to the retained descriptor, so renaming the directory does not
// retarget the capability.
type Directory struct {
	file     *os.File
	path     string
	ownerUID int
	ownerGID int
	logger   *diagnostic.Logger
}

var _ io.Closer = (*Directory)(nil)

// DirectoryOptions identifies the expected owner of Toby-managed directories
// and the destination for best-effort permission-repair diagnostics.
type DirectoryOptions struct {
	OwnerUID int
	OwnerGID int
	Logger   *diagnostic.Logger
}

// Path returns the original diagnostic path used to open the capability. The
// directory descriptor remains authoritative if that path is later renamed.
func (d *Directory) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// LockMode selects shared or exclusive advisory flock behavior.
type LockMode uint8

const (
	// LockShared permits other shared lock holders.
	LockShared LockMode = iota + 1

	// LockExclusive excludes both shared and exclusive lock holders.
	LockExclusive
)

// Lock retains the open file description that owns an advisory flock.
type Lock struct {
	file   *os.File
	logger *diagnostic.Logger
}

var _ io.Closer = (*Lock)(nil)
