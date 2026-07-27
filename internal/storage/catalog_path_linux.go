//go:build linux

package storage

// Resolves a retained directory capability to its current kernel path.

import (
	"fmt"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func resolvedDirectoryPath(
	directory *safefs.Directory,
) (path string, returnErr error) {
	file, err := directory.File()
	if err != nil {
		return "", err
	}
	defer func() {
		diagnostic.DiscardError(
			"closing a diagnostic path descriptor cannot change path resolution",
			"close resolved directory path descriptor",
			file.Close(),
			"path", directory.Path(),
		)
	}()

	path, err = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return "", err
	}
	return path, nil
}
