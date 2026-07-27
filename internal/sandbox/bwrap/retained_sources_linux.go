//go:build linux

package bwrap

// Retains an independently owned descriptor set for all host capabilities used
// across the sequential Bubblewrap invocations in one run.

import (
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/mount"
)

// retainedSources owns one duplicate of every caller-provided source. The
// caller's higher-level OCI and storage leases must still outlive the run.
type retainedSources struct {
	mu      sync.Mutex
	sources Sources
	files   []*os.File
	closed  bool
}

var _ io.Closer = (*retainedSources)(nil)

func retainPlanSources(
	plan Plan,
	sources Sources,
) (*retainedSources, error) {
	if err := validateSources(plan, sources); err != nil {
		return nil, err
	}

	retained := &retainedSources{
		sources: Sources{
			ManagedDirectories: make(map[mount.Key]*os.File),
			Projects:           make(map[string]*os.File),
			Binds:              make(map[string]*os.File),
			BindParents:        make(map[string]*os.File),
			BindNames:          make(map[string]string),
			RuntimeAssets:      make(map[string]*os.File),
		},
	}
	retain := func(source *os.File, label string) (*os.File, error) {
		duplicate, err := duplicateDescriptor(source, label)
		if err != nil {
			return nil, err
		}
		retained.files = append(retained.files, duplicate)
		return duplicate, nil
	}
	fail := func(err error) (*retainedSources, error) {
		diagnostic.DiscardError(
			"source retention has no diagnostic logger",
			"close partially retained Bubblewrap run sources",
			retained.Close(),
		)
		return nil, err
	}

	var err error
	retained.sources.ProtectedRoots.ImageStore, err = retain(
		sources.ProtectedRoots.ImageStore,
		"run OCI image-store root",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.ProtectedRoots.PersistentData, err = retain(
		sources.ProtectedRoots.PersistentData,
		"run Toby persistent-data root",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.ProtectedRoots.RunStorage, err = retain(
		sources.ProtectedRoots.RunStorage,
		"run Bubblewrap run-storage root",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.ProtectedRoots.Runtime, err = retain(
		sources.ProtectedRoots.Runtime,
		"run Toby runtime root",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.RootFS, err = retain(sources.RootFS, "run rootfs")
	if err != nil {
		return fail(err)
	}
	retained.sources.OverlayUpper, err = retain(
		sources.OverlayUpper,
		"run overlay upper",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.OverlayWork, err = retain(
		sources.OverlayWork,
		"run overlay work",
	)
	if err != nil {
		return fail(err)
	}
	retained.sources.Home, err = retain(sources.Home, "run private home")
	if err != nil {
		return fail(err)
	}

	for _, entry := range plan.ManagedDirectories {
		retained.sources.ManagedDirectories[entry.Key], err = retain(
			sources.ManagedDirectories[entry.Key],
			"run managed-directory "+entry.Key.String(),
		)
		if err != nil {
			return fail(err)
		}
	}
	for _, project := range plan.Projects {
		retained.sources.Projects[project.Name], err = retain(
			sources.Projects[project.Name],
			"run project "+project.Name,
		)
		if err != nil {
			return fail(err)
		}
	}
	for _, bind := range plan.Binds {
		retained.sources.Binds[bind.Target], err = retain(
			sources.Binds[bind.Target],
			"run external bind "+bind.Target,
		)
		if err != nil {
			return fail(err)
		}
		retained.sources.BindParents[bind.Target], err = retain(
			sources.BindParents[bind.Target],
			"run external bind "+bind.Target+" parent",
		)
		if err != nil {
			return fail(err)
		}
		retained.sources.BindNames[bind.Target] =
			sources.BindNames[bind.Target]
	}
	for _, asset := range plan.RuntimeAssets {
		retained.sources.RuntimeAssets[asset.Target], err = retain(
			sources.RuntimeAssets[asset.Target],
			"run runtime asset "+asset.Target,
		)
		if err != nil {
			return fail(err)
		}
	}
	retained.sources.SandboxBinary, err = retain(
		sources.SandboxBinary,
		"run sandbox helper",
	)
	if err != nil {
		return fail(err)
	}

	return retained, nil
}

func (s *retainedSources) current() (Sources, error) {
	if s == nil {
		return Sources{}, os.ErrInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Sources{}, os.ErrInvalid
	}

	return s.sources, nil
}

func (s *retainedSources) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	for index, file := range s.files {
		if file != nil {
			diagnostic.DiscardError(
				"releasing a retained sandbox source is cleanup",
				"close retained sandbox source",
				file.Close(),
				"source_index", index,
			)
			s.files[index] = nil
		}
	}
	s.files = nil
	s.sources = Sources{}

	return nil
}
