package toolfiles

// Maps validated sandbox targets to private-home or managed-directory backing
// identities and detached Bubblewrap plan records.

import (
	"fmt"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

type backingID struct {
	home       bool
	managedKey mount.Key
}

type backingPlan struct {
	id       backingID
	target   string
	hostPath string
}

type resolvedFile struct {
	file      File
	backing   backingID
	relative  string
	directory *safefs.Directory
}

func validateFiles(files []File, identity bwrap.Identity) error {
	for index, file := range files {
		if err := file.validate(); err != nil {
			return fmt.Errorf("file %d: %w", index, err)
		}
		if file.UID != identity.HostUID || file.GID != identity.HostGID {
			return fmt.Errorf(
				"file %d owned by %q has uid:gid %d:%d, want plan host identity %d:%d",
				index,
				file.Owner,
				file.UID,
				file.GID,
				identity.HostUID,
				identity.HostGID,
			)
		}
		for earlier := range index {
			if mount.TargetsOverlap(files[earlier].Target, file.Target) {
				return fmt.Errorf(
					"file targets %q owned by %q and %q owned by %q overlap",
					files[earlier].Target,
					files[earlier].Owner,
					file.Target,
					file.Owner,
				)
			}
		}
	}

	return nil
}

func resolveFiles(
	plan bwrap.Plan,
	files []File,
) ([]resolvedFile, []bwrap.GeneratedFile, error) {
	backings := make([]backingPlan, 0, len(plan.ManagedDirectories)+1)
	backings = append(backings, backingPlan{
		id:       backingID{home: true},
		target:   layout.Home,
		hostPath: plan.Home.HostPath,
	})
	for _, entry := range plan.ManagedDirectories {
		backings = append(backings, backingPlan{
			id:       backingID{managedKey: entry.Key},
			target:   entry.Target,
			hostPath: entry.HostPath,
		})
	}

	resolved := make([]resolvedFile, len(files))
	generated := make([]bwrap.GeneratedFile, len(files))
	for index, file := range files {
		backing, relative, err := resolveBacking(file.Target, backings)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"resolve %q owned by %q: %w",
				file.Target,
				file.Owner,
				err,
			)
		}

		resolved[index] = resolvedFile{
			file:     file,
			backing:  backing.id,
			relative: filepath.FromSlash(relative),
		}
		generated[index] = bwrap.GeneratedFile{
			HostPath: filepath.Join(
				backing.hostPath,
				filepath.FromSlash(relative),
			),
			Target: file.Target,
			Data:   append([]byte(nil), file.Data...),
			Mode:   file.Mode,
			UID:    file.UID,
			GID:    file.GID,
		}
	}

	return resolved, generated, nil
}

func resolveBacking(
	target string,
	backings []backingPlan,
) (backingPlan, string, error) {
	var home *backingPlan
	var managed []backingPlan
	for index := range backings {
		backing := &backings[index]
		if !strictlyBeneath(backing.target, target) {
			continue
		}
		if backing.id.home {
			home = backing
		} else {
			managed = append(managed, *backing)
		}
	}

	if len(managed) > 1 {
		return backingPlan{}, "", fmt.Errorf(
			"target %q maps through %d managed-directory backings, want exactly one",
			target,
			len(managed),
		)
	}

	var selected backingPlan
	switch {
	case len(managed) == 1:
		selected = managed[0]
	case home != nil:
		selected = *home
	default:
		return backingPlan{}, "", fmt.Errorf(
			"target %q is not beneath the private home or exactly one managed directory",
			target,
		)
	}

	return selected, strings.TrimPrefix(target, selected.target+"/"), nil
}

func strictlyBeneath(parent, child string) bool {
	return strings.HasPrefix(child, parent+"/")
}

func cloneGeneratedFiles(files []bwrap.GeneratedFile) []bwrap.GeneratedFile {
	clone := make([]bwrap.GeneratedFile, len(files))
	for index, file := range files {
		clone[index] = file
		clone[index].Data = append([]byte(nil), file.Data...)
	}
	return clone
}
