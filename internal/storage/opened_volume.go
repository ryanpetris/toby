package storage

// Retains one verified volume data directory and its shared lifecycle lease.

import (
	"errors"
	"io"

	"petris.dev/toby/internal/storage/safefs"
)

type openedVolume struct {
	data  *safefs.Directory
	lease *safefs.Lock
}

var _ io.Closer = (*openedVolume)(nil)

func (v *openedVolume) Close() error {
	if v == nil {
		return nil
	}

	var err error
	if v.data != nil {
		err = v.data.Close()
	}
	v.data = nil
	if v.lease != nil {
		err = errors.Join(err, v.lease.Close())
	}
	v.lease = nil
	return err
}
