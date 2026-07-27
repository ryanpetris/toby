package sidecar

// Opens, validates, duplicates, and closes pinned mount descriptors.

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/mount"
)

func openMount(
	name string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	descriptor, err := unix.Open(
		name,
		unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), "sidecar mount capability")
	if file == nil {
		logger.DebugError(
			"close invalid sidecar mount descriptor",
			unix.Close(descriptor),
		)
		return nil, fmt.Errorf(
			"open sidecar mount: invalid descriptor",
		)
	}

	return file, nil
}

func validateMountSource(
	source *os.File,
	access mount.Access,
) error {
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect source descriptor: %w", err)
	}

	mode := info.Mode()
	if access == mount.AccessDev {
		if mode&os.ModeSocket == 0 {
			return fmt.Errorf(
				"device-access source must be a Unix socket",
			)
		}
		return nil
	}
	if mode.IsDir() ||
		mode.IsRegular() ||
		mode&os.ModeSocket != 0 {
		return nil
	}

	return fmt.Errorf("source has unsupported filesystem type")
}

func duplicateFile(
	source *os.File,
	logger *diagnostic.Logger,
) (*os.File, error) {
	descriptor, err := unix.FcntlInt(
		source.Fd(),
		unix.F_DUPFD_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	duplicate := os.NewFile(uintptr(descriptor), "sidecar mount duplicate")
	if duplicate == nil {
		logger.DebugError(
			"close invalid sidecar mount duplicate",
			unix.Close(descriptor),
		)
		return nil, fmt.Errorf(
			"duplicate sidecar mount: invalid descriptor",
		)
	}

	return duplicate, nil
}

func closeFiles(files map[string]*os.File) error {
	var closeErr error
	for target, file := range files {
		if file != nil {
			closeErr = errors.Join(closeErr, file.Close())
			delete(files, target)
		}
	}

	return closeErr
}
