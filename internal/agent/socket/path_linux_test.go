//go:build linux

package socket

// Verifies agent root following and socket-entry identity protections.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestElectRejectsInvalidPaths(t *testing.T) {
	longName := strings.Repeat("x", maxUnixPathBytes)

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: "runtime/toby/agent.sock"},
		{name: "unclean", path: t.TempDir() + "/runtime/../runtime/toby/agent.sock"},
		{name: "root parent", path: "/agent.sock"},
		{name: "too long", path: "/" + longName},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Elect(t.Context(), test.path, Options{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Elect error = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestElectRejectsParentThatIsNotDirectory(t *testing.T) {
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(runtimeDirectory, "toby")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(parent, "agent.sock")
	if _, err := Elect(t.Context(), path, Options{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Elect error = %v, want ErrUnsafePath", err)
	}
}

func TestElectAcceptsExistingModeAndSymlinkedAncestors(t *testing.T) {
	root := t.TempDir()
	realRuntime := filepath.Join(root, "real-runtime")
	if err := os.Mkdir(realRuntime, 0o750); err != nil {
		t.Fatalf("create real runtime: %v", err)
	}
	linkedRuntime := filepath.Join(root, "linked-runtime")
	if err := os.Symlink(realRuntime, linkedRuntime); err != nil {
		t.Fatalf("link runtime directory: %v", err)
	}

	path := filepath.Join(linkedRuntime, "toby", "agent.sock")
	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("symlinked runtime did not elect a listener")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(realRuntime, "toby")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("created agent directory mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestElectCreatesExternalParentsWithUmaskAndSecuresEndpointRoot(
	t *testing.T,
) {
	root, err := os.MkdirTemp("", "toby-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(
		root,
		"missing",
		"runtime",
		"toby",
		"agent.sock",
	)

	var election *Election
	err = func() error {
		previous := unix.Umask(0o027)
		defer unix.Umask(previous)

		var err error
		election, err = Elect(t.Context(), path, Options{})
		return err
	}()
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("missing runtime ancestry did not elect a listener")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(root, "missing"), mode: 0o750},
		{path: filepath.Join(root, "missing", "runtime"), mode: 0o750},
		{path: filepath.Join(root, "missing", "runtime", "toby"), mode: 0o700},
	} {
		info, err := os.Stat(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != test.mode {
			t.Errorf(
				"%s mode = %04o, want %04o",
				test.path,
				info.Mode().Perm(),
				test.mode,
			)
		}
	}
}

func TestElectRepairsSymlinkedEndpointParent(t *testing.T) {
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "runtime")
	target := filepath.Join(root, "agent-target")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(runtimeDirectory, "toby")
	if err := os.Symlink(target, parent); err != nil {
		t.Fatal(err)
	}

	election, err := Elect(
		t.Context(),
		filepath.Join(parent, "agent.sock"),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("symlinked endpoint parent did not elect a listener")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf(
			"resolved endpoint root mode = %04o, want 0700",
			info.Mode().Perm(),
		)
	}
	if info, err := os.Lstat(parent); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("endpoint-root symlink was replaced")
	}
	if _, err := os.Lstat(filepath.Join(target, "agent.sock")); !os.IsNotExist(err) {
		t.Fatalf("listener socket remains in symlink target: %v", err)
	}
}

func TestElectRepairsExistingEndpointParentMode(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}

	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(parent); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf(
			"repaired endpoint root mode = %04o, want 0700",
			info.Mode().Perm(),
		)
	}
}

func TestElectAcceptsUnexpectedParentOwner(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("create endpoint parent: %v", err)
	}

	election, err := elect(
		t.Context(),
		path,
		uint32(os.Geteuid()+1),
		uint32(os.Getegid()),
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("election did not create a listener")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestElectRejectsArbitraryEndpointObjects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) string
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, path string) string {
				t.Helper()
				if err := os.WriteFile(path, []byte("preserve me"), 0o600); err != nil {
					t.Fatalf("create endpoint file: %v", err)
				}
				return path
			},
		},
		{
			name: "symbolic link",
			setup: func(t *testing.T, path string) string {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("preserve me"), 0o600); err != nil {
					t.Fatalf("create endpoint target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create endpoint symlink: %v", err)
				}
				return target
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := testSocketPath(t)
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("create endpoint parent: %v", err)
			}
			preserved := test.setup(t, path)

			if _, err := Elect(t.Context(), path, Options{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Elect error = %v, want ErrUnsafePath", err)
			}
			content, err := os.ReadFile(preserved)
			if err != nil {
				t.Fatalf("read preserved object: %v", err)
			}
			if got := string(content); got != "preserve me" {
				t.Fatalf("preserved content = %q, want %q", got, "preserve me")
			}
		})
	}
}

func TestElectRejectsUnsafeElectionLock(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "symbolic link",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "lock-target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("create lock target: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("create lock symlink: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := testSocketPath(t)
			parent := filepath.Dir(path)
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatalf("create endpoint parent: %v", err)
			}
			lockPath := filepath.Join(parent, "."+filepath.Base(path)+electionLockSuffix)
			test.setup(t, lockPath)

			if _, err := Elect(t.Context(), path, Options{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Elect error = %v, want ErrUnsafePath", err)
			}
			if _, err := os.Lstat(lockPath); err != nil {
				t.Fatalf("unsafe lock was removed: %v", err)
			}
		})
	}
}

func TestElectRepairsPermissiveElectionLock(t *testing.T) {
	path := testSocketPath(t)
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("create endpoint parent: %v", err)
	}
	lockPath := filepath.Join(parent, "."+filepath.Base(path)+electionLockSuffix)
	if err := os.WriteFile(lockPath, nil, 0o640); err != nil {
		t.Fatalf("create permissive lock: %v", err)
	}

	election, err := Elect(t.Context(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("election did not create a listener")
	}
	if err := election.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(lockPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %04o, want 0600", info.Mode().Perm())
	}
}
