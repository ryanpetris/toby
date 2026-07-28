package toolfiles

// Preflights existing native parent paths without mutation, creates missing
// safe parents, and performs per-file atomic replacements.

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func preflightFile(
	file resolvedFile,
	logger *diagnostic.Logger,
) error {
	parent, _ := filepath.Split(file.relative)
	parent = strings.TrimSuffix(parent, string(filepath.Separator))

	directory, complete, err := openExistingParent(
		file.directory,
		parent,
		logger,
	)
	if err != nil {
		return err
	}
	if !complete {
		return nil
	}
	defer func() {
		logger.DebugError(
			"close generated-file preflight directory",
			directory.Close(),
			"path", parent,
		)
	}()
	return nil
}

func openExistingParent(
	root *safefs.Directory,
	parent string,
	logger *diagnostic.Logger,
) (*safefs.Directory, bool, error) {
	current, err := root.Duplicate()
	if err != nil {
		return nil, false, err
	}
	if parent == "" {
		return current, true, nil
	}

	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		next, openErr := current.OpenDirectory(component)
		if errors.Is(openErr, fs.ErrNotExist) {
			logger.DebugError(
				"close incomplete generated-file parent",
				current.Close(),
				"path", parent,
			)
			return nil, false, nil
		}
		if openErr != nil {
			logger.DebugError(
				"close generated-file parent after traversal failed",
				current.Close(),
				"path", parent,
			)
			return nil, false, openErr
		}

		logger.DebugError(
			"close traversed generated-file parent",
			current.Close(),
			"path", parent,
		)
		current = next
	}

	return current, true, nil
}

func writeFile(
	file resolvedFile,
	logger *diagnostic.Logger,
) error {
	parent, base := filepath.Split(file.relative)
	parent = strings.TrimSuffix(parent, string(filepath.Separator))

	directory, err := createParent(
		file.directory,
		parent,
		logger,
	)
	if err != nil {
		return err
	}
	defer func() {
		logger.DebugError(
			"close generated-file parent",
			directory.Close(),
			"path", parent,
		)
	}()

	return directory.ReplaceFileOwned(
		base,
		file.file.Data,
		file.file.Mode,
		file.file.UID,
		file.file.GID,
	)
}

func createParent(
	root *safefs.Directory,
	parent string,
	logger *diagnostic.Logger,
) (*safefs.Directory, error) {
	current, err := root.Duplicate()
	if err != nil {
		return nil, err
	}
	if parent == "" {
		return current, nil
	}

	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		next, openErr := current.OpenDirectory(component)
		if errors.Is(openErr, fs.ErrNotExist) {
			next, openErr = current.MkdirAll(component)
		}
		if openErr != nil {
			logger.DebugError(
				"close generated-file parent after creation failed",
				current.Close(),
				"path", parent,
			)
			return nil, openErr
		}

		logger.DebugError(
			"close traversed generated-file parent",
			current.Close(),
			"path", parent,
		)
		current = next
	}

	return current, nil
}
