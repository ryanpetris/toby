//go:build linux

package safefs

// Enumerates direct children through an independently opened directory stream.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

const directoryReadBatch = 128

// Names returns the sorted names of direct children without following them.
// maxEntries bounds the number retained in memory.
func (d *Directory) Names(maxEntries uint64) ([]string, error) {
	if maxEntries == 0 {
		return nil, fmt.Errorf("directory entry limit must be positive")
	}

	fd, err := d.openIndependent()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), d.Path())
	defer func() {
		d.logger.DebugError(
			"close safe-filesystem directory listing",
			file.Close(),
			"path", d.Path(),
		)
	}()

	var names []string
	for {
		entries, err := file.ReadDir(directoryReadBatch)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if uint64(len(names))+uint64(len(entries)) > maxEntries {
			return nil, fmt.Errorf(
				"%w: reading directory %q",
				ErrLimitExceeded,
				d.Path(),
			)
		}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}

	sort.Strings(names)
	return names, nil
}
