//go:build linux

package pasta

// Waits for Bubblewrap to publish the child user namespace that owns its
// private network namespace.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const (
	namespacePollInterval = time.Millisecond
	namespaceReadyTimeout = time.Second
)

func waitForChildUserNamespace(
	ctx context.Context,
	targetPID int,
) error {
	host, err := namespaceIdentity("/proc/self/ns/user")
	if err != nil {
		return fmt.Errorf("inspect host user namespace: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, namespaceReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(namespacePollInterval)
	defer ticker.Stop()

	target := filepath.Join(
		"/proc",
		strconv.Itoa(targetPID),
		"ns",
		"user",
	)
	var latestErr error
	for {
		child, statErr := namespaceIdentity(target)
		switch {
		case statErr == nil && child != host:
			parent, parentErr := parentUserNamespaceIdentity(target)
			if parentErr == nil && parent != host {
				return nil
			}
			if parentErr != nil &&
				!errors.Is(parentErr, os.ErrNotExist) {
				latestErr = parentErr
			}
		case statErr == nil:
		case errors.Is(statErr, os.ErrNotExist):
		default:
			latestErr = statErr
		}

		select {
		case <-readyCtx.Done():
			if latestErr != nil {
				return errors.Join(readyCtx.Err(), latestErr)
			}
			return readyCtx.Err()
		case <-ticker.C:
		}
	}
}

type namespaceID struct {
	device uint64
	inode  uint64
}

func namespaceIdentity(path string) (namespaceID, error) {
	info, err := os.Stat(path)
	if err != nil {
		return namespaceID{}, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return namespaceID{}, fmt.Errorf(
			"namespace metadata has unexpected type %T",
			info.Sys(),
		)
	}
	return namespaceID{
		device: uint64(stat.Dev),
		inode:  stat.Ino,
	}, nil
}

func parentUserNamespaceIdentity(
	path string,
) (namespaceID, error) {
	namespace, err := os.Open(path)
	if err != nil {
		return namespaceID{}, err
	}
	defer func() {
		diagnostic.DiscardError(
			"namespace readiness does not depend on descriptor cleanup",
			"close Bubblewrap user namespace descriptor",
			namespace.Close(),
			"path", path,
		)
	}()

	parentFD, err := unix.IoctlRetInt(
		int(namespace.Fd()),
		unix.NS_GET_PARENT,
	)
	if err != nil {
		return namespaceID{}, err
	}
	parent := os.NewFile(
		uintptr(parentFD),
		"Bubblewrap parent user namespace",
	)
	defer func() {
		diagnostic.DiscardError(
			"namespace readiness does not depend on descriptor cleanup",
			"close Bubblewrap parent user namespace descriptor",
			parent.Close(),
			"path", path,
		)
	}()

	parentID, err := fileNamespaceIdentity(parent)
	if err != nil {
		return namespaceID{}, err
	}
	return parentID, nil
}

func fileNamespaceIdentity(file *os.File) (namespaceID, error) {
	info, err := file.Stat()
	if err != nil {
		return namespaceID{}, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return namespaceID{}, fmt.Errorf(
			"namespace metadata has unexpected type %T",
			info.Sys(),
		)
	}
	return namespaceID{
		device: uint64(stat.Dev),
		inode:  stat.Ino,
	}, nil
}
