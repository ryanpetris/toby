//go:build linux

package bwrap

// Exercises descriptor-authoritative external-bind isolation, exact retained
// parent relationships, and hard-link rejection.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestRenderRejectsExternalBindAliasesToOwnedDirectories(t *testing.T) {
	tests := []struct {
		name   string
		source func(Sources) *os.File
	}{
		{
			name: "private home",
			source: func(sources Sources) *os.File {
				return sources.Home
			},
		},
		{
			name: "rootfs",
			source: func(sources Sources) *os.File {
				return sources.RootFS
			},
		},
		{
			name: "overlay upper",
			source: func(sources Sources) *os.File {
				return sources.OverlayUpper
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			source := test.source(sources)
			parent, err := openDescriptorParent(
				source,
				"malicious external bind",
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := parent.Close(); err != nil &&
					!errors.Is(err, os.ErrInvalid) {
					t.Error(err)
				}
			})

			target := "/opt/malicious-bind"
			plan.Binds = []mount.Bind{{
				HostPath: filepath.Join(
					"/external",
					filepath.Base(source.Name()),
				),
				Target: target,
				Access: mount.AccessReadOnly,
			}}
			sources.Binds[target] = source
			sources.BindParents[target] = parent

			assertExternalBindRejected(
				t,
				plan,
				sources,
				"overlaps protected path rooted at",
			)
		})
	}
}

func TestRenderRejectsExternalRegularFileBeneathProtectedStorage(
	t *testing.T,
) {
	plan := validPlan()
	sources := rendererSources(t, plan)
	sourcePath := filepath.Join(sources.Home.Name(), "protected.conf")
	if err := os.WriteFile(sourcePath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := openRegularSourceAt(t, sourcePath)
	parent := openDirectorySourceAt(t, sources.Home.Name())

	const target = "/etc/protected.conf"
	plan.Binds = []mount.Bind{{
		HostPath: "/external/protected.conf",
		Target:   target,
		Access:   mount.AccessReadOnly,
	}}
	sources.Binds[target] = source
	sources.BindParents[target] = parent

	assertExternalBindRejected(
		t,
		plan,
		sources,
		"external bind /etc/protected.conf parent source lineage overlaps",
	)
}

func TestRenderRejectsExternalBindSourceParentMismatch(t *testing.T) {
	plan := validPlan()
	sources := rendererSources(t, plan)

	firstParent := t.TempDir()
	firstPath := filepath.Join(firstParent, "config")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondParent := t.TempDir()
	secondPath := filepath.Join(secondParent, "config")
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	const target = "/etc/config"
	plan.Binds = []mount.Bind{{
		HostPath: "/external/config",
		Target:   target,
		Access:   mount.AccessReadOnly,
	}}
	sources.Binds[target] = openRegularSourceAt(t, firstPath)
	sources.BindParents[target] = openDirectorySourceAt(t, secondParent)

	assertExternalBindRejected(
		t,
		plan,
		sources,
		"is not the exact basename child of its retained parent",
	)
}

func TestRenderRejectsExternalBindHardLinkAlias(t *testing.T) {
	plan := validPlan()
	sources := rendererSources(t, plan)

	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "config")
	if err := os.WriteFile(sourcePath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, filepath.Join(t.TempDir(), "alias")); err != nil {
		t.Fatal(err)
	}

	const target = "/etc/config"
	plan.Binds = []mount.Bind{{
		HostPath: "/external/config",
		Target:   target,
		Access:   mount.AccessReadOnly,
	}}
	sources.Binds[target] = openRegularSourceAt(t, sourcePath)
	sources.BindParents[target] = openDirectorySourceAt(t, parent)

	assertExternalBindRejected(
		t,
		plan,
		sources,
		"unsafe link count",
	)
}

func TestRenderAllowsLegitimateExternalRegularFileAndSocket(t *testing.T) {
	tests := []struct {
		name   string
		target string
		access mount.Access
		open   func(*testing.T) (string, *os.File, *os.File)
	}{
		{
			name:   "resolver mount file",
			target: "/etc/resolv.conf",
			access: mount.AccessReadOnly,
			open: func(t *testing.T) (string, *os.File, *os.File) {
				parentPath := t.TempDir()
				sourcePath := filepath.Join(parentPath, "resolv.conf")
				if err := os.WriteFile(
					sourcePath,
					[]byte("nameserver 192.0.2.1\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				return sourcePath,
					openRegularSourceAt(t, sourcePath),
					openDirectorySourceAt(t, parentPath)
			},
		},
		{
			name:   "Unix socket",
			target: "/var/run/service.sock",
			access: mount.AccessDev,
			open: func(t *testing.T) (string, *os.File, *os.File) {
				parentPath := t.TempDir()
				sourcePath := filepath.Join(parentPath, "service.sock")
				return sourcePath,
					openSocketSourceAt(t, sourcePath),
					openDirectorySourceAt(t, parentPath)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			sourcePath, source, parent := test.open(t)
			plan.Binds = []mount.Bind{{
				HostPath: sourcePath,
				Target:   test.target,
				Access:   test.access,
			}}
			sources.Binds[test.target] = source
			sources.BindParents[test.target] = parent

			invocation, err := Render(plan, sources)
			if err != nil {
				t.Fatal(err)
			}
			if err := invocation.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func openRegularSourceAt(t *testing.T, path string) *os.File {
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

func assertExternalBindRejected(
	t *testing.T,
	plan Plan,
	sources Sources,
	match string,
) {
	t.Helper()

	invocation, err := Render(plan, sources)
	if invocation != nil {
		t.Cleanup(func() {
			if err := invocation.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), match) {
		t.Fatalf("Render error = %v, want text %q", err, match)
	}
}
