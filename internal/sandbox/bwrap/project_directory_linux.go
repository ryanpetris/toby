//go:build linux

package bwrap

// Opens exact nested selected-project directory capabilities without
// re-resolving pathnames after the run has retained its source descriptors.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func (r *Run) openProjectDirectory(
	name string,
	relative string,
) (*os.File, error) {
	if r == nil {
		return nil, fmt.Errorf("bubblewrap run is nil")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.sources == nil {
		return nil, fmt.Errorf("bubblewrap run is closed")
	}

	sources, err := r.sources.current()
	if err != nil {
		return nil, fmt.Errorf("access Bubblewrap run sources: %w", err)
	}
	source, found := sources.Projects[name]
	if !found {
		return nil, fmt.Errorf("project %q has no retained source", name)
	}
	if relative == "" {
		return duplicateDescriptor(source, "project "+name)
	}
	if err := validateProjectRelativePath(relative); err != nil {
		return nil, err
	}

	return openProjectDirectoryBeneath(
		source,
		name,
		relative,
		r.logger,
	)
}

func openProjectDirectoryBeneath(
	source *os.File,
	name string,
	relative string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	how := &unix.OpenHow{
		Flags: unix.O_RDONLY |
			unix.O_DIRECTORY |
			unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS,
	}
	fd, err := unix.Openat2(int(source.Fd()), relative, how)
	if err == nil {
		return os.NewFile(
			uintptr(fd),
			"project "+name+"/"+filepath.ToSlash(relative),
		), nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return nil, fmt.Errorf(
			"open repository %q beneath project %q: %w",
			filepath.ToSlash(relative),
			name,
			err,
		)
	}

	return walkProjectDirectory(source, name, relative, logger)
}

func walkProjectDirectory(
	source *os.File,
	name string,
	relative string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	current, err := duplicateDescriptor(source, "project "+name)
	if err != nil {
		return nil, err
	}

	for _, component := range strings.Split(
		relative,
		string(filepath.Separator),
	) {
		nextFD, openErr := unix.Openat(
			int(current.Fd()),
			component,
			unix.O_RDONLY|
				unix.O_DIRECTORY|
				unix.O_CLOEXEC|
				unix.O_NOFOLLOW,
			0,
		)
		logger.DebugError(
			"close traversed project directory descriptor",
			current.Close(),
			"project", name,
		)
		if openErr != nil {
			return nil, fmt.Errorf(
				"open repository %q beneath project %q: %w",
				filepath.ToSlash(relative),
				name,
				openErr,
			)
		}

		current = os.NewFile(
			uintptr(nextFD),
			"project "+name+"/"+filepath.ToSlash(relative),
		)
	}

	return current, nil
}

func validateProjectRelativePath(relative string) error {
	if relative == "" || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative ||
		strings.ContainsRune(relative, 0) {
		return fmt.Errorf("invalid project-relative repository path %q", relative)
	}
	for _, component := range strings.Split(
		relative,
		string(filepath.Separator),
	) {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf(
				"invalid project-relative repository path %q",
				relative,
			)
		}
	}

	return nil
}
