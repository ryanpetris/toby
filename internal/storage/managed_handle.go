package storage

// Retains one exact verified tool-volume capability and its immutable resolved
// mount entry.

import (
	"errors"
	"io"
	"os"

	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

// ManagedHandle retains one verified tool-volume backing.
type ManagedHandle struct {
	entry     mount.Entry
	directory *safefs.Directory
	lease     *safefs.Lock
}

var _ io.Closer = (*ManagedHandle)(nil)

// Entry returns a copy of the validated native mount entry.
func (h *ManagedHandle) Entry() mount.Entry {
	if h == nil {
		return mount.Entry{}
	}
	return h.entry
}

// File duplicates the opened tool-volume capability for a caller.
func (h *ManagedHandle) File() (*os.File, error) {
	if h == nil || h.directory == nil {
		return nil, os.ErrInvalid
	}
	return h.directory.File()
}

// Close releases the retained tool-volume capability.
func (h *ManagedHandle) Close() error {
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

func newManagedHandle(
	profile string,
	request mount.Request,
	volume *openedVolume,
) *ManagedHandle {
	return &ManagedHandle{
		entry: mount.Entry{
			Key:      request.Key,
			Profile:  profile,
			HostPath: volume.data.Path(),
			Target:   request.Target,
			Access:   request.Access,
			Optional: request.Optional,
			Seed:     request.Seed,
		},
		directory: volume.data,
		lease:     volume.lease,
	}
}
