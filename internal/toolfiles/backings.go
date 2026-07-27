package toolfiles

// Opens and validates the authoritative private-home and managed-directory
// capabilities selected for one native file publication.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

func openBackings(
	plan bwrap.Plan,
	sources bwrap.Sources,
	logger *diagnostic.Logger,
) (map[backingID]*safefs.Directory, error) {
	if sources.Home == nil {
		return nil, fmt.Errorf("private-home source descriptor is nil")
	}
	if err := validateManagedSources(plan, sources.ManagedDirectories); err != nil {
		return nil, err
	}

	type source struct {
		id             backingID
		diagnosticPath string
		file           *os.File
	}
	planned := make([]source, 0, len(plan.ManagedDirectories)+1)
	planned = append(planned, source{
		id:             backingID{home: true},
		diagnosticPath: plan.Home.HostPath,
		file:           sources.Home,
	})
	for _, entry := range plan.ManagedDirectories {
		planned = append(planned, source{
			id:             backingID{managedKey: entry.Key},
			diagnosticPath: entry.HostPath,
			file:           sources.ManagedDirectories[entry.Key],
		})
	}

	result := make(map[backingID]*safefs.Directory, len(planned))
	identities := make([]fs.FileInfo, 0, len(planned))
	for _, source := range planned {
		directory, err := safefs.OpenDirectoryFile(
			source.file,
			source.diagnosticPath,
			safefs.DirectoryOptions{
				OwnerUID: plan.Identity.HostUID,
				OwnerGID: plan.Identity.HostGID,
				Logger:   logger,
			},
		)
		if err != nil {
			logger.DebugError(
				"close native backings after open failed",
				closeBackings(result),
			)
			return nil, fmt.Errorf(
				"open native backing %q: %w",
				source.diagnosticPath,
				err,
			)
		}

		descriptor, err := directory.File()
		if err != nil {
			logger.DebugError(
				"close native backing after identity retention failed",
				directory.Close(),
				"path", source.diagnosticPath,
			)
			logger.DebugError(
				"close native backings after identity retention failed",
				closeBackings(result),
			)
			return nil, fmt.Errorf(
				"retain native backing identity %q: %w",
				source.diagnosticPath,
				err,
			)
		}
		info, statErr := descriptor.Stat()
		closeErr := descriptor.Close()
		if statErr != nil {
			logger.DebugError(
				"close native backing identity descriptor",
				closeErr,
				"path", source.diagnosticPath,
			)
			logger.DebugError(
				"close native backing after identity inspection failed",
				directory.Close(),
				"path", source.diagnosticPath,
			)
			logger.DebugError(
				"close native backings after identity inspection failed",
				closeBackings(result),
			)
			return nil, fmt.Errorf(
				"inspect native backing identity %q: %w",
				source.diagnosticPath,
				statErr,
			)
		}
		logger.DebugError(
			"close native backing identity descriptor",
			closeErr,
			"path", source.diagnosticPath,
		)
		for index, other := range identities {
			if os.SameFile(info, other) {
				logger.DebugError(
					"close aliased native backing",
					directory.Close(),
					"path", source.diagnosticPath,
				)
				logger.DebugError(
					"close native backings after alias validation failed",
					closeBackings(result),
				)
				return nil, fmt.Errorf(
					"native backing %q aliases backing %d",
					source.diagnosticPath,
					index,
				)
			}
		}

		identities = append(identities, info)
		result[source.id] = directory
	}

	return result, nil
}

func validateManagedSources(
	plan bwrap.Plan,
	sources map[mount.Key]*os.File,
) error {
	expected := make(map[mount.Key]struct{}, len(plan.ManagedDirectories))
	for _, entry := range plan.ManagedDirectories {
		expected[entry.Key] = struct{}{}
		if sources[entry.Key] == nil {
			return fmt.Errorf(
				"managed-directory source %q is missing or nil",
				entry.Key,
			)
		}
	}
	for key := range sources {
		if _, found := expected[key]; !found {
			return fmt.Errorf("unexpected managed-directory source %q", key)
		}
	}

	return nil
}

func closeBackings(backings map[backingID]*safefs.Directory) error {
	var closeErr error
	for id, directory := range backings {
		if directory != nil {
			if err := directory.Close(); err != nil {
				closeErr = errors.Join(
					closeErr,
					fmt.Errorf("close native backing %v: %w", id, err),
				)
			}
		}
	}
	return closeErr
}
