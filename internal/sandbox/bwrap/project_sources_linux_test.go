//go:build linux

package bwrap

// Exercises descriptor-authoritative project isolation from complete per-user
// storage roots across symbolic links, ancestry, and path-replacement races.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderRejectsProjectSymlinksIntoProtectedRoots(t *testing.T) {
	tests := []struct {
		name       string
		sourcePath func(*testing.T, Sources) string
		match      string
	}{
		{
			name: "selected rootfs",
			sourcePath: func(t *testing.T, sources Sources) string {
				return sources.RootFS.Name()
			},
			match: "protected path rooted at OCI image-store root",
		},
		{
			name: "sibling rootfs snapshot",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.ImageStore.Name(),
					"rootfs",
					"unselected",
				)
			},
			match: "protected path rooted at OCI image-store root",
		},
		{
			name: "blob store",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.ImageStore.Name(),
					"blobs",
					"sha256",
					"other",
				)
			},
			match: "protected path rooted at OCI image-store root",
		},
		{
			name: "image temporary storage",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.ImageStore.Name(),
					"tmp",
					"download",
				)
			},
			match: "protected path rooted at OCI image-store root",
		},
		{
			name: "sibling Toby volume",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.PersistentData.Name(),
					"volumes",
					"other-id",
					"_data",
				)
			},
			match: "protected path rooted at Toby persistent-data root",
		},
		{
			name: "unselected tool volume",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.PersistentData.Name(),
					"volumes",
					"other-id",
					"_data",
				)
			},
			match: "protected path rooted at Toby persistent-data root",
		},
		{
			name: "runtime root",
			sourcePath: func(_ *testing.T, sources Sources) string {
				return sources.ProtectedRoots.Runtime.Name()
			},
			match: "protected path rooted at Toby runtime root",
		},
		{
			name: "runtime caddy descendant",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.Runtime.Name(),
					"caddy",
					"admin",
				)
			},
			match: "protected path rooted at Toby runtime root",
		},
		{
			name: "sibling run",
			sourcePath: func(t *testing.T, sources Sources) string {
				return makeProjectTestDirectory(
					t,
					sources.ProtectedRoots.RunStorage.Name(),
					"other-run",
					"upper",
				)
			},
			match: "protected path rooted at Bubblewrap run-storage root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			link := projectSymlink(t, test.sourcePath(t, sources))

			setProjectSource(t, &plan, &sources, link, link)
			assertProjectSourceRejected(t, plan, sources, test.match)
		})
	}
}

func TestRenderRejectsRealProjectAncestorsOfProtectedRoots(t *testing.T) {
	tests := []struct {
		name     string
		ancestor func(Sources) string
		match    string
	}{
		{
			name: "image store parent",
			ancestor: func(sources Sources) string {
				return filepath.Dir(sources.ProtectedRoots.ImageStore.Name())
			},
			match: "protected path rooted at OCI image-store root",
		},
		{
			name: "run storage parent",
			ancestor: func(sources Sources) string {
				return filepath.Dir(sources.ProtectedRoots.RunStorage.Name())
			},
			match: "protected path rooted at Bubblewrap run-storage root",
		},
		{
			name: "runtime parent",
			ancestor: func(sources Sources) string {
				return filepath.Dir(sources.ProtectedRoots.Runtime.Name())
			},
			match: "protected path rooted at Toby runtime root",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			ancestor := test.ancestor(sources)

			setProjectSource(t, &plan, &sources, ancestor, ancestor)
			assertProjectSourceRejected(t, plan, sources, test.match)
		})
	}
}

func TestRenderRejectsProjectDescriptorPathReplacementIntoRuntime(t *testing.T) {
	plan := validPlan()
	if plan.Projects[0].ReadOnly {
		t.Fatal("test requires a read-write project")
	}
	sources := rendererSources(t, plan)
	harmlessConfiguredPath := t.TempDir()
	runtimeSocketParent := makeProjectTestDirectory(
		t,
		sources.ProtectedRoots.Runtime.Name(),
		"runs",
		"other",
		"mcp",
	)

	setProjectSource(
		t,
		&plan,
		&sources,
		harmlessConfiguredPath,
		runtimeSocketParent,
	)
	assertProjectSourceRejected(
		t,
		plan,
		sources,
		"protected path rooted at Toby runtime root",
	)
}

func TestRenderRequiresEveryAuthoritativeProtectedRoot(t *testing.T) {
	tests := []struct {
		name  string
		clear func(*ProtectedRoots)
		match string
	}{
		{
			name: "image store",
			clear: func(roots *ProtectedRoots) {
				roots.ImageStore = nil
			},
			match: "OCI image-store root source descriptor is nil",
		},
		{
			name: "persistent data",
			clear: func(roots *ProtectedRoots) {
				roots.PersistentData = nil
			},
			match: "Toby persistent-data root source descriptor is nil",
		},
		{
			name: "run storage",
			clear: func(roots *ProtectedRoots) {
				roots.RunStorage = nil
			},
			match: "Bubblewrap run-storage root source descriptor is nil",
		},
		{
			name: "runtime",
			clear: func(roots *ProtectedRoots) {
				roots.Runtime = nil
			},
			match: "Toby runtime root source descriptor is nil",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			test.clear(&sources.ProtectedRoots)

			assertProjectSourceRejected(t, plan, sources, test.match)
		})
	}
}

func TestRenderRejectsDecoyProtectedRootDescriptors(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, *ProtectedRoots)
		match   string
	}{
		{
			name: "image store",
			replace: func(t *testing.T, roots *ProtectedRoots) {
				roots.ImageStore = openDirectorySource(t)
			},
			match: "OCI image-store root is not strictly beneath the Toby persistent-data root",
		},
		{
			name: "persistent data",
			replace: func(t *testing.T, roots *ProtectedRoots) {
				roots.PersistentData = openDirectorySource(t)
			},
			match: "OCI image-store root is not strictly beneath the Toby persistent-data root",
		},
		{
			name: "run storage",
			replace: func(t *testing.T, roots *ProtectedRoots) {
				roots.RunStorage = openDirectorySource(t)
			},
			match: "overlay run root is not a direct child",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			test.replace(t, &sources.ProtectedRoots)

			assertProjectSourceRejected(t, plan, sources, test.match)
		})
	}
}

func TestRenderAllowsProjectOutsideProtectedRoots(t *testing.T) {
	t.Run("direct path", func(t *testing.T) {
		plan := validPlan()
		sources := rendererSources(t, plan)

		assertProjectSourceAllowed(t, plan, sources)
	})

	t.Run("symbolic link", func(t *testing.T) {
		plan := validPlan()
		sources := rendererSources(t, plan)
		project := sources.Projects[plan.Projects[0].Name]
		link := projectSymlink(t, project.Name())
		setProjectSource(t, &plan, &sources, link, link)

		assertProjectSourceAllowed(t, plan, sources)
	})
}

func makeProjectTestDirectory(
	t *testing.T,
	root string,
	components ...string,
) string {
	t.Helper()

	directory := filepath.Join(append([]string{root}, components...)...)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	return directory
}

func projectSymlink(t *testing.T, target string) string {
	t.Helper()

	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	return link
}

func setProjectSource(
	t *testing.T,
	plan *Plan,
	sources *Sources,
	diagnosticPath string,
	descriptorPath string,
) {
	t.Helper()

	source := openProjectTestDirectory(t, descriptorPath)
	project := &plan.Projects[0]
	project.HostPath = diagnosticPath
	sources.Projects[project.Name] = source
}

func openProjectTestDirectory(t *testing.T, path string) *os.File {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Error(err)
		}
	})

	return file
}

func assertProjectSourceRejected(
	t *testing.T,
	plan Plan,
	sources Sources,
	match string,
) {
	t.Helper()

	invocation, err := Render(plan, sources)
	if invocation != nil {
		if closeErr := invocation.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("protected project source produced a Bubblewrap invocation")
	}
	if err == nil || !strings.Contains(err.Error(), match) {
		t.Fatalf("Render error = %v, want text %q", err, match)
	}
}

func assertProjectSourceAllowed(
	t *testing.T,
	plan Plan,
	sources Sources,
) {
	t.Helper()

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
}
