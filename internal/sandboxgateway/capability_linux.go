//go:build linux

package sandboxgateway

// Pins one sandbox socket generation for descriptor-backed Bubblewrap binding.

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

// Capability retains the exact socket generation selected for one launch.
type Capability struct {
	mu sync.Mutex

	hostSocket    string
	sandboxSocket string
	source        *os.File
	closed        bool
}

var _ io.Closer = (*Capability)(nil)

// OpenCapability validates and pins the exact socket object without following
// a final-component symbolic link.
func OpenCapability(config DescriptorConfig) (*Capability, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	descriptor, err := unix.Open(
		config.HostSocket,
		unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open sandbox gateway capability: %w",
			err,
		)
	}

	source := os.NewFile(
		uintptr(descriptor),
		"sandbox gateway capability",
	)
	if source == nil {
		err := fmt.Errorf(
			"open sandbox gateway capability: invalid descriptor",
		)
		diagnostic.DiscardError(
			"capability setup already failed",
			"close sandbox gateway capability descriptor",
			unix.Close(descriptor),
		)
		return nil, err
	}
	if err := validateCapabilitySource(
		source,
		config,
	); err != nil {
		diagnostic.DiscardError(
			"capability validation already failed",
			"close sandbox gateway capability source",
			source.Close(),
		)
		return nil, err
	}

	return &Capability{
		hostSocket:    config.HostSocket,
		sandboxSocket: config.SandboxSocket,
		source:        source,
	}, nil
}

// HostSocket returns the diagnostic host path while the capability is open.
func (c *Capability) HostSocket() string {
	if c == nil {
		return ""
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ""
	}

	return c.hostSocket
}

// SandboxSocket returns the fixed sandbox mount target while open.
func (c *Capability) SandboxSocket() string {
	if c == nil {
		return ""
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ""
	}

	return c.sandboxSocket
}

// File returns a caller-owned duplicate of the pinned socket descriptor.
func (c *Capability) File() (*os.File, error) {
	if c == nil {
		return nil, fmt.Errorf(
			"sandbox gateway capability is nil",
		)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.source == nil {
		return nil, fmt.Errorf(
			"sandbox gateway capability is closed",
		)
	}

	descriptor, err := unix.FcntlInt(
		c.source.Fd(),
		unix.F_DUPFD_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"duplicate sandbox gateway capability: %w",
			err,
		)
	}

	duplicate := os.NewFile(
		uintptr(descriptor),
		"sandbox gateway capability duplicate",
	)
	if duplicate == nil {
		err := fmt.Errorf(
			"duplicate sandbox gateway capability: invalid descriptor",
		)
		diagnostic.DiscardError(
			"capability duplication already failed",
			"close duplicate sandbox gateway capability descriptor",
			unix.Close(descriptor),
		)
		return nil, err
	}

	return duplicate, nil
}

// Close drops the launch-side reference.
func (c *Capability) Close() error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true

	err := c.source.Close()
	c.source = nil
	c.hostSocket = ""
	c.sandboxSocket = ""

	return err
}

func validateCapabilitySource(
	source *os.File,
	config DescriptorConfig,
) error {
	var status unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &status); err != nil {
		return fmt.Errorf(
			"inspect sandbox gateway capability: %w",
			err,
		)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf(
			"sandbox gateway capability is not a Unix socket",
		)
	}
	if uint64(status.Dev) != config.HostSocketDevice ||
		status.Ino != config.HostSocketInode {
		return fmt.Errorf(
			"sandbox gateway capability generation changed before pinning",
		)
	}

	return nil
}
