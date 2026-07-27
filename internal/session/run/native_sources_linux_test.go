//go:build linux

package run

// Exercises descriptor-authoritative project and external-bind source opening.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools"
)

func TestOpenNativeProjectsRejectsRootBoundSymlinkOutsideProjectRoot(
	t *testing.T,
) {
	base := t.TempDir()
	projectRoot := filepath.Join(base, "Projects")
	external := filepath.Join(base, "external")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(projectRoot, "escape")
	if err := os.Symlink(external, source); err != nil {
		t.Fatal(err)
	}

	_, _, err := openNativeProjects([]tools.ProjectMount{{
		Name:               "escape",
		Source:             source,
		RequireProjectRoot: true,
	}}, projectRoot, nil)
	if err == nil || !strings.Contains(
		err.Error(),
		"resolves outside XDG_PROJECTS_DIR",
	) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenNativeProjectsAcceptsRootBoundProjectAtOrBelowProjectRoot(
	t *testing.T,
) {
	projectRoot := t.TempDir()
	descendant := filepath.Join(projectRoot, "team", "app")
	if err := os.MkdirAll(descendant, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "equal", source: projectRoot},
		{name: "descendant", source: descendant},
	} {
		t.Run(test.name, func(t *testing.T) {
			projects, _, err := openNativeProjects(
				[]tools.ProjectMount{{
					Name:               test.name,
					Source:             test.source,
					RequireProjectRoot: true,
				}},
				projectRoot,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := closeNativeProjects(projects); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenNativeProjectsComparesAgainstRetainedProjectRoot(
	t *testing.T,
) {
	base := t.TempDir()
	projectRootPath := filepath.Join(base, "Projects")
	originalProject := filepath.Join(projectRootPath, "app")
	if err := os.MkdirAll(originalProject, 0o755); err != nil {
		t.Fatal(err)
	}

	projectRoot, err := openNativeDirectory(
		projectRootPath,
		"retained project root",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer projectRoot.Close()

	retainedPath := filepath.Join(base, "retained-Projects")
	if err := os.Rename(projectRootPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	replacementProject := filepath.Join(projectRootPath, "app")
	if err := os.MkdirAll(replacementProject, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err = openNativeProjectsWithRoot(
		[]tools.ProjectMount{{
			Name:               "app",
			Source:             replacementProject,
			RequireProjectRoot: true,
		}},
		projectRoot,
		nil,
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"resolves outside XDG_PROJECTS_DIR",
	) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenNativeProjectsAllowsExplicitExternalProject(
	t *testing.T,
) {
	external := t.TempDir()
	projects, _, err := openNativeProjects(
		[]tools.ProjectMount{{
			Name:   "external",
			Source: external,
		}},
		filepath.Join(t.TempDir(), "missing-project-root"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeNativeProjects(projects); err != nil {
		t.Fatal(err)
	}
}

func TestOpenNativeBindsFollowsSymbolicLinkAncestor(t *testing.T) {
	base := t.TempDir()
	actualParent := filepath.Join(base, "actual")
	if err := os.Mkdir(actualParent, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(actualParent, "config")
	if err := os.WriteFile(sourcePath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked")
	if err := os.Symlink(actualParent, link); err != nil {
		t.Fatal(err)
	}

	binds, err := openNativeBinds([]mount.Bind{{
		HostPath: filepath.Join(link, "config"),
		Target:   "/etc/config",
		Access:   mount.AccessReadOnly,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeNativeBinds(binds); err != nil {
			t.Error(err)
		}
	})
	if len(binds) != 1 {
		t.Fatalf("opened binds = %d, want 1", len(binds))
	}
	if got := binds[0].Bind.HostPath; got != filepath.Join(link, "config") {
		t.Fatalf("declared host path = %q", got)
	}
	if got := binds[0].ResolvedName; got != filepath.Base(sourcePath) {
		t.Fatalf("resolved name = %q, want %q", got, filepath.Base(sourcePath))
	}
}

func TestOpenNativeBindsPreservesLegitimateKindsAndOptionality(
	t *testing.T,
) {
	t.Run("regular mount file", func(t *testing.T) {
		resolver := filepath.Join(t.TempDir(), "resolv.conf")
		if err := os.WriteFile(
			resolver,
			[]byte("nameserver 192.0.2.1\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		binds, err := openNativeBinds([]mount.Bind{{
			HostPath: resolver,
			Target:   "/etc/resolv.conf",
			Access:   mount.AccessReadOnly,
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeNativeBinds(binds); err != nil {
				t.Error(err)
			}
		})
		if len(binds) != 1 {
			t.Fatalf("opened binds = %d, want 1", len(binds))
		}
		info, err := binds[0].Source.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s mode = %v, want regular file", resolver, info.Mode())
		}
	})

	t.Run("optional directory", func(t *testing.T) {
		base := t.TempDir()
		present := filepath.Join(base, "present")
		if err := os.Mkdir(present, 0o700); err != nil {
			t.Fatal(err)
		}
		binds, err := openNativeBinds([]mount.Bind{
			{
				HostPath: filepath.Join(base, "missing"),
				Target:   "/opt/missing",
				Access:   mount.AccessReadOnly,
				Optional: true,
			},
			{
				HostPath: present,
				Target:   "/opt/present",
				Access:   mount.AccessReadOnly,
				Optional: true,
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeNativeBinds(binds); err != nil {
				t.Error(err)
			}
		})
		if len(binds) != 1 || binds[0].Bind.Target != "/opt/present" {
			t.Fatalf("opened optional binds = %#v", binds)
		}
		info, err := binds[0].Source.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Fatalf("optional source mode = %v, want directory", info.Mode())
		}
	})

	t.Run("final symbolic link", func(t *testing.T) {
		base := t.TempDir()
		actual := filepath.Join(base, "actual")
		if err := os.WriteFile(actual, []byte("config"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "linked")
		if err := os.Symlink(filepath.Base(actual), link); err != nil {
			t.Fatal(err)
		}

		binds, err := openNativeBinds([]mount.Bind{{
			HostPath: link,
			Target:   "/etc/config",
			Access:   mount.AccessReadOnly,
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeNativeBinds(binds); err != nil {
				t.Error(err)
			}
		})
		if len(binds) != 1 {
			t.Fatalf("opened binds = %d, want 1", len(binds))
		}
		if got := binds[0].Bind.HostPath; got != link {
			t.Fatalf("declared host path = %q", got)
		}
		if got := binds[0].ResolvedName; got != filepath.Base(actual) {
			t.Fatalf(
				"resolved name = %q, want %q",
				got,
				filepath.Base(actual),
			)
		}
	})

	t.Run("Unix socket", func(t *testing.T) {
		socketPath := filepath.Join(t.TempDir(), "service.sock")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})

		binds, err := openNativeBinds([]mount.Bind{{
			HostPath: socketPath,
			Target:   "/run/service.sock",
			Access:   mount.AccessDev,
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := closeNativeBinds(binds); err != nil {
				t.Error(err)
			}
		})
		if len(binds) != 1 {
			t.Fatalf("opened binds = %d, want 1", len(binds))
		}
		info, err := binds[0].Source.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("socket source mode = %v", info.Mode())
		}
	})
}

func TestOpenNativeBindsRejectsMagicLink(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(sourcePath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})

	binds, err := openNativeBinds([]mount.Bind{{
		HostPath: "/proc/self/fd/" + strconv.FormatUint(
			uint64(source.Fd()),
			10,
		),
		Target: "/etc/config",
		Access: mount.AccessReadOnly,
	}}, nil)
	if len(binds) != 0 {
		if closeErr := closeNativeBinds(binds); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatalf("opened binds = %d, want 0", len(binds))
	}
	if !errors.Is(err, unix.ELOOP) {
		t.Fatalf("magic-link error = %v, want ELOOP", err)
	}
}

func TestCloseNativeBindsClosesSourceAndParentDescriptors(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(sourcePath, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	binds, err := openNativeBinds([]mount.Bind{{
		HostPath: sourcePath,
		Target:   "/etc/config",
		Access:   mount.AccessReadOnly,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 {
		t.Fatalf("opened binds = %d, want 1", len(binds))
	}
	sourceFD := int(binds[0].Source.Fd())
	parentFD := int(binds[0].Parent.Fd())

	if err := closeNativeBinds(binds); err != nil {
		t.Fatal(err)
	}
	for _, fd := range []int{sourceFD, parentFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); !errors.Is(err, unix.EBADF) {
			t.Fatalf("descriptor %d remains open: %v", fd, err)
		}
	}
}

func TestOpenNativeBindsCleansDescriptorsAfterLaterFailure(t *testing.T) {
	base := t.TempDir()
	valid := filepath.Join(base, "valid")
	if err := os.Mkdir(valid, 0o700); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}

	before := nativeOpenDescriptorCount(t)
	for range 32 {
		binds, err := openNativeBinds([]mount.Bind{
			{
				HostPath: valid,
				Target:   "/opt/valid",
				Access:   mount.AccessReadOnly,
			},
			{
				HostPath: filepath.Join(link, "missing"),
				Target:   "/opt/rejected",
				Access:   mount.AccessReadOnly,
			},
		}, nil)
		if len(binds) != 0 {
			if closeErr := closeNativeBinds(binds); closeErr != nil {
				t.Error(closeErr)
			}
			t.Fatalf("opened binds = %d, want 0", len(binds))
		}
		if err == nil {
			t.Fatal("symlinked later bind was accepted")
		}
	}
	after := nativeOpenDescriptorCount(t)
	if after != before {
		t.Fatalf("open descriptors after failures = %d, want %d", after, before)
	}
}

func nativeOpenDescriptorCount(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
