package toolfiles

// Collects native tool-file registrations and returns detached deterministic
// snapshots for launch planning.

import (
	"fmt"
	"sort"

	"petris.dev/toby/internal/sandbox/mount"
)

// Registry is one launch's collection of Toby-owned native files.
type Registry struct {
	files []File
}

// NewRegistry creates an empty native tool-file registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds one native file after validating its independent fields and
// ensuring that its target does not equal, contain, or sit beneath another
// registered file target.
func (r *Registry) Register(file File) error {
	if r == nil {
		return fmt.Errorf("register native tool file: registry is nil")
	}
	if err := file.validate(); err != nil {
		return fmt.Errorf("register native tool file: %w", err)
	}
	for _, existing := range r.files {
		if mount.TargetsOverlap(existing.Target, file.Target) {
			return fmt.Errorf(
				"register native tool file: targets %q owned by %q and %q owned by %q overlap",
				existing.Target,
				existing.Owner,
				file.Target,
				file.Owner,
			)
		}
	}

	r.files = append(r.files, file.clone())

	return nil
}

// Files returns a detached deterministic snapshot ordered by target and owner.
func (r *Registry) Files() []File {
	if r == nil {
		return nil
	}

	files := cloneFiles(r.files)
	sortFiles(files)
	return files
}

func cloneFiles(files []File) []File {
	clone := make([]File, len(files))
	for index, file := range files {
		clone[index] = file.clone()
	}
	return clone
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Target == files[j].Target {
			return files[i].Owner < files[j].Owner
		}
		return files[i].Target < files[j].Target
	})
}
