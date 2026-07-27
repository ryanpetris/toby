//go:build linux

package oci

// Retains one immutable rootfs descriptor and exposes caller-owned duplicates
// while its Prepared lease remains open.

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

// Prepared retains one immutable rootfs descriptor lease.
type Prepared struct {
	mu sync.Mutex

	metadata Metadata
	path     string
	rootfs   *os.File
	lease    *safefs.Lock
	logger   *diagnostic.Logger
}

var _ io.Closer = (*Prepared)(nil)

func newPrepared(
	metadata Metadata,
	path string,
	rootfs *os.File,
	lease *safefs.Lock,
	logger *diagnostic.Logger,
) *Prepared {
	return &Prepared{
		metadata: cloneMetadata(metadata),
		path:     path,
		rootfs:   rootfs,
		lease:    lease,
		logger:   logger,
	}
}

// Metadata returns a detached copy of the prepared image identity.
func (p *Prepared) Metadata() Metadata {
	if p == nil {
		return Metadata{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return cloneMetadata(p.metadata)
}

// Spec returns a detached copy of the prepared rootfs configuration.
func (p *Prepared) Spec() Spec {
	if p == nil {
		return Spec{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return cloneSpec(p.metadata.Spec)
}

// RootfsPath returns the non-authoritative diagnostic path of the immutable
// rootfs.
func (p *Prepared) RootfsPath() string {
	if p == nil {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rootfs == nil {
		return ""
	}

	return p.path
}

// RootfsFile returns a caller-owned CLOEXEC duplicate of the rootfs directory.
// Prepared must remain open while the duplicate or a sandbox bound from it is
// in use.
func (p *Prepared) RootfsFile() (*os.File, error) {
	if p == nil {
		return nil, os.ErrInvalid
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rootfs == nil {
		return nil, os.ErrInvalid
	}

	fd, err := unix.FcntlInt(
		p.rootfs.Fd(),
		unix.F_DUPFD_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("duplicate OCI rootfs descriptor: %w", err)
	}

	return os.NewFile(uintptr(fd), p.path), nil
}

// Close releases the retained rootfs descriptor.
func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	rootfs := p.rootfs
	lease := p.lease
	p.rootfs = nil
	p.lease = nil
	p.mu.Unlock()
	if rootfs == nil && lease == nil {
		return nil
	}

	if rootfs != nil {
		p.logger.DebugError(
			"close prepared OCI rootfs",
			rootfs.Close(),
			"path", p.path,
		)
	}
	if lease != nil {
		p.logger.DebugError(
			"release prepared OCI lease",
			lease.Close(),
			"path", p.path,
		)
	}
	return nil
}
