//go:build linux

package bwrap

// Recovers bounded batches of published Bubblewrap run directories without
// crossing a live run's lifetime flock.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func recoverPublishedRuns(
	root *safefs.Directory,
	limits RunStorageLimits,
	logger *diagnostic.Logger,
) error {
	names, truncated, err := readRunStorageNames(
		root,
		limits.MaxRecoveryCandidates,
		logger,
	)
	if err != nil {
		return err
	}

	for _, name := range names {
		if name == runMutationLockName ||
			strings.HasPrefix(name, runPublicationTemporary) {
			continue
		}
		if !generatedRunIDPattern.MatchString(name) {
			return fmt.Errorf(
				"%w: unexpected Bubblewrap run-storage entry %q",
				safefs.ErrUnsafePath,
				name,
			)
		}

		candidate, err := root.OpenDirectory(name)
		if err != nil {
			return fmt.Errorf("open abandoned Bubblewrap run %q: %w", name, err)
		}
		lifetime, lockErr := candidate.Lock(
			runLifetimeLockName,
			safefs.LockExclusive,
			true,
		)
		candidateCloseErr := candidate.Close()
		if errors.Is(lockErr, safefs.ErrWouldBlock) {
			if candidateCloseErr != nil {
				return candidateCloseErr
			}
			continue
		}
		if lockErr != nil || candidateCloseErr != nil {
			return errors.Join(lockErr, candidateCloseErr)
		}

		removeErr := root.RemoveAll(name, limits.MaxCleanupEntries)
		lockCloseErr := lifetime.Close()
		if removeErr != nil || lockCloseErr != nil {
			if removeErr != nil {
				removeErr = fmt.Errorf(
					"remove abandoned Bubblewrap run %q: %w",
					name,
					removeErr,
				)
			}
			return errors.Join(
				removeErr,
				lockCloseErr,
			)
		}
	}

	if truncated {
		return fmt.Errorf(
			"%w: Bubblewrap run recovery exceeds %d candidates",
			safefs.ErrLimitExceeded,
			limits.MaxRecoveryCandidates,
		)
	}
	return nil
}

func readRunStorageNames(
	directory *safefs.Directory,
	maxEntries int,
	logger *diagnostic.Logger,
) (names []string, truncated bool, returnErr error) {
	if maxEntries <= 0 {
		return nil, false, fmt.Errorf("run recovery limit must be positive")
	}

	authority, err := directory.File()
	if err != nil {
		return nil, false, err
	}
	fd, err := unix.Openat(
		int(authority.Fd()),
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	authorityCloseErr := authority.Close()
	logger.DebugError(
		"close Bubblewrap run-storage authority",
		authorityCloseErr,
	)
	if err != nil {
		if fd >= 0 {
			closeDescriptor(logger, fd)
		}
		return nil, false, &fs.PathError{
			Op:   "reopen Bubblewrap run storage",
			Path: directory.Path(),
			Err:  err,
		}
	}

	file := os.NewFile(uintptr(fd), directory.Path())
	defer func() {
		logger.DebugError(
			"close Bubblewrap run-storage listing",
			file.Close(),
		)
	}()
	for {
		batch, readErr := file.Readdirnames(64)
		for _, name := range batch {
			if name == "." || name == ".." || strings.ContainsRune(name, 0) {
				return nil, false, fmt.Errorf(
					"%w: invalid Bubblewrap run-storage entry %q",
					safefs.ErrUnsafePath,
					name,
				)
			}
			if len(names) == maxEntries {
				sort.Strings(names)
				return names, true, nil
			}
			names = append(names, name)
		}
		if errors.Is(readErr, io.EOF) {
			sort.Strings(names)
			return names, false, nil
		}
		if readErr != nil {
			return nil, false, &fs.PathError{
				Op:   "read Bubblewrap run storage",
				Path: directory.Path(),
				Err:  readErr,
			}
		}
	}
}
