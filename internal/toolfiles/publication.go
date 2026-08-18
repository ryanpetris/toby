package toolfiles

// Preflights existing native parent paths, applies declared patches, creates
// missing safe parents, and performs per-file atomic replacements.

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/configpatch"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/storage/safefs"
)

const maxPatchSourceBytes = 1 << 20

func applyPatches(
	files []resolvedFile,
	generated []bwrap.GeneratedFile,
	logger *diagnostic.Logger,
) error {
	for index := range files {
		file := &files[index]
		if file.file.Patch.Empty() {
			continue
		}

		source, err := readPatchSource(*file, logger)
		if err != nil {
			return fmt.Errorf(
				"read %q owned by %q: %w",
				file.file.Target,
				file.file.Owner,
				err,
			)
		}
		patched, err := applyPatchData(file.file.Target, source, file.file.Patch)
		if err != nil {
			return fmt.Errorf(
				"apply patch to %q owned by %q: %w",
				file.file.Target,
				file.file.Owner,
				err,
			)
		}
		file.file.Data = patched
		generated[index].Data = append([]byte(nil), patched...)
	}
	return nil
}

func readPatchSource(file resolvedFile, logger *diagnostic.Logger) ([]byte, error) {
	parent, base := filepath.Split(file.relative)
	parent = strings.TrimSuffix(parent, string(filepath.Separator))

	directory, complete, err := openExistingParent(
		file.directory,
		parent,
		logger,
	)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, nil
	}
	defer func() {
		logger.DebugError(
			"close generated-file patch source directory",
			directory.Close(),
			"path", parent,
		)
	}()

	data, err := directory.ReadFile(base, maxPatchSourceBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func applyPatchData(target string, source []byte, patch configpatch.Patch) ([]byte, error) {
	format, err := patchFormat(target)
	if err != nil {
		return nil, err
	}
	switch format {
	case patchFormatTOML:
		return configpatch.ApplyTOML(source, patch)
	case patchFormatJSON:
		return configpatch.ApplyJSON(source, patch)
	default:
		return nil, fmt.Errorf("unsupported patch format %q", format)
	}
}

type patchFormatName string

const (
	patchFormatTOML patchFormatName = "toml"
	patchFormatJSON patchFormatName = "json"
)

func patchFormat(target string) (patchFormatName, error) {
	switch {
	case strings.HasSuffix(target, ".toml"):
		return patchFormatTOML, nil
	case strings.HasSuffix(target, ".json"):
		return patchFormatJSON, nil
	default:
		return "", fmt.Errorf("patch target %q must end in .toml or .json", target)
	}
}

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
