package storage

// Retains the exact verified home-volume data capability selected for a
// launch.

import (
	"errors"
	"io"
	"os"

	"petris.dev/toby/internal/storage/safefs"
)

// HomeHandle retains a verified private-home backing directory.
type HomeHandle struct {
	identity  HomeIdentity
	hostPath  string
	directory *safefs.Directory
	lease     *safefs.Lock
}

var _ io.Closer = (*HomeHandle)(nil)

// Identity returns the immutable private-home identity selected by this
// handle.
func (h *HomeHandle) Identity() HomeIdentity {
	if h == nil {
		return HomeIdentity{}
	}
	return h.identity
}

// HostPath returns the diagnostic native path for the retained home
// directory. File remains the authoritative capability for execution.
func (h *HomeHandle) HostPath() string {
	if h == nil {
		return ""
	}
	return h.hostPath
}

// File duplicates the opened home-directory capability for a caller.
func (h *HomeHandle) File() (*os.File, error) {
	if h == nil || h.directory == nil {
		return nil, os.ErrInvalid
	}
	return h.directory.File()
}

// Close releases the retained home-directory capabilities.
func (h *HomeHandle) Close() error {
	if h == nil {
		return nil
	}

	var err error
	if h.directory != nil {
		err = h.directory.Close()
	}
	h.directory = nil
	if h.lease != nil {
		err = errors.Join(err, h.lease.Close())
	}
	h.lease = nil
	return err
}

func newHomeHandle(
	identity HomeIdentity,
	volume *openedVolume,
) *HomeHandle {
	return &HomeHandle{
		identity:  identity,
		hostPath:  volume.data.Path(),
		directory: volume.data,
		lease:     volume.lease,
	}
}
