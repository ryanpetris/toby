//go:build linux

package storage

// Verifies bounded, normalized, cancellable seed copying and fail-closed
// handling of unsupported source entries.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/storage/safefs"
)

func TestSeedCopyHonorsEntryBytePathAndDepthBounds(t *testing.T) {
	tests := []struct {
		name   string
		limits func() Limits
		build  func(*testing.T, string)
	}{
		{
			name: "entries",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.SeedEntries = 1
				return limits
			},
			build: func(t *testing.T, directory string) {
				writeSeedFile(t, directory, "one", "")
				writeSeedFile(t, directory, "two", "")
			},
		},
		{
			name: "regular bytes",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.SeedBytes = 3
				return limits
			},
			build: func(t *testing.T, directory string) {
				writeSeedFile(t, directory, "data", "four")
			},
		},
		{
			name: "symlink bytes",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.SeedBytes = 3
				return limits
			},
			build: func(t *testing.T, directory string) {
				if err := os.Symlink("four", filepath.Join(directory, "link")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "path bytes",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.PathBytes = 8
				return limits
			},
			build: func(t *testing.T, directory string) {
				writeSeedFile(t, directory, "123456789", "")
			},
		},
		{
			name: "depth",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.Depth = 1
				return limits
			},
			build: func(t *testing.T, directory string) {
				nested := filepath.Join(directory, "one", "two")
				if err := os.MkdirAll(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				writeSeedFile(t, nested, "data", "x")
			},
		},
		{
			name: "hardlink metadata",
			limits: func() Limits {
				limits := DefaultLimits()
				limits.MetadataSize = 8
				return limits
			},
			build: func(t *testing.T, directory string) {
				source := filepath.Join(directory, "long-hardlink-source")
				writeSeedFile(t, directory, "long-hardlink-source", "linked")
				if err := os.Link(source, filepath.Join(directory, "long-hardlink-copy")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := secureStorageTestPath(t)
			root, directory := makeHomeSeedRoot(t, base)
			test.build(t, directory)

			service := newStorageTestStoreWithLimits(t, base, test.limits())
			_, err := service.ResolveHome(
				t.Context(),
				test.name,
				defaultProfile,
				testStorageSeedSource(
					t,
					root,
					"/seed",
				),
			)
			if !errors.Is(err, ErrSeedLimitExceeded) {
				t.Fatalf("ResolveHome error = %v, want ErrSeedLimitExceeded", err)
			}
			assertHomeWasNotPublished(t, service, test.name)
		})
	}
}

func TestSeedCopyPreservesFilesystemModes(t *testing.T) {
	base := secureStorageTestPath(t)
	root, source := makeHomeSeedRoot(t, base)
	directory := filepath.Join(source, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "program")
	writeSeedFile(t, directory, "program", "executable")
	link := filepath.Join(source, "current")
	if err := os.Symlink("directory/program", link); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() == 0 {
		if err := os.Chown(file, 12345, 12345); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(link, 12345, 12345); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(file, 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755|os.ModeSetgid|os.ModeSticky); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	home, err := service.ResolveHome(
		t.Context(),
		"normalized",
		defaultProfile,
		testStorageSeedSource(
			t,
			root,
			"/seed",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	destinationDirectory := filepath.Join(home.HostPath(), "directory")
	destinationFile := filepath.Join(destinationDirectory, "program")
	destinationLink := filepath.Join(home.HostPath(), "current")
	for _, path := range []string{destinationDirectory, destinationFile, destinationLink} {
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil {
			t.Fatal(err)
		}
		if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
			t.Errorf("%s ownership = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, os.Geteuid(), os.Getegid())
		}
	}

	var directoryStat unix.Stat_t
	if err := unix.Lstat(destinationDirectory, &directoryStat); err != nil {
		t.Fatal(err)
	}
	if directoryStat.Mode&0o7777 != 0o3755 {
		t.Fatalf("destination directory mode = %04o, want 3755", directoryStat.Mode&0o7777)
	}

	var fileStat unix.Stat_t
	if err := unix.Lstat(destinationFile, &fileStat); err != nil {
		t.Fatal(err)
	}
	if fileStat.Mode&0o777 != 0o755 {
		t.Fatalf("destination file mode = %04o, want 0755", fileStat.Mode&0o777)
	}
}

func TestSeedCopySurvivesRestrictiveUmask(t *testing.T) {
	base := secureStorageTestPath(t)
	root, source := makeHomeSeedRoot(t, base)
	nested := filepath.Join(source, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSeedFile(t, nested, "value", "seeded")

	service := newStorageTestStore(t, base)
	seed := testStorageSeedSource(
		t,
		root,
		"/seed",
	)

	var home *HomeHandle
	err := func() error {
		previous := unix.Umask(0o777)
		defer unix.Umask(previous)

		var err error
		home, err = service.ResolveHome(
			t.Context(),
			"restrictive-umask",
			defaultProfile,
			seed,
		)
		return err
	}()
	if err != nil {
		t.Fatalf("ResolveHome under umask 0777: %v", err)
	}
	defer home.Close()

	data, err := os.ReadFile(filepath.Join(home.HostPath(), "one", "two", "value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "seeded" {
		t.Fatalf("seeded value = %q", data)
	}
}

func TestSeedCopyRejectsFIFODeviceAndSocketWithoutPublication(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T, string)
	}{
		{
			name: "fifo",
			build: func(t *testing.T, directory string) {
				if err := unix.Mkfifo(filepath.Join(directory, "fifo"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "device",
			build: func(t *testing.T, directory string) {
				err := unix.Mknod(
					filepath.Join(directory, "device"),
					unix.S_IFCHR|0o600,
					int(unix.Mkdev(1, 3)),
				)
				if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
					t.Skipf("creating a device node is not permitted: %v", err)
				}
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "socket",
			build: func(t *testing.T, directory string) {
				socket := filepath.Join(directory, "socket")
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					listener.Close()
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := secureStorageTestPath(t)
			root, directory := makeHomeSeedRoot(t, base)
			test.build(t, directory)

			service := newStorageTestStore(t, base)
			_, err := service.ResolveHome(
				t.Context(),
				test.name,
				defaultProfile,
				testStorageSeedSource(
					t,
					root,
					"/seed",
				),
			)
			if !errors.Is(err, ErrUnsupportedSeed) {
				t.Fatalf("ResolveHome error = %v, want ErrUnsupportedSeed", err)
			}
			assertHomeWasNotPublished(t, service, test.name)
		})
	}
}

func TestSeedCopyRejectsReplacedSourcePathWithoutPublication(t *testing.T) {
	base := secureStorageTestPath(t)
	root := filepath.Join(base, "rootfs")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSeedFile(t, outside, "secret", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "seed")); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	_, err := service.ResolveHome(
		t.Context(),
		"replacement",
		defaultProfile,
		testStorageSeedSource(
			t,
			root,
			"/seed",
		),
	)
	if !errors.Is(err, safefs.ErrUnsafePath) {
		t.Fatalf("ResolveHome error = %v, want safefs.ErrUnsafePath", err)
	}
	assertHomeWasNotPublished(t, service, "replacement")
}

func TestSeedCopyUsesExactRootDescriptorAfterPathReplacement(t *testing.T) {
	base := secureStorageTestPath(t)
	root, source := makeHomeSeedRoot(t, base)
	writeSeedFile(t, source, "state", "leased")
	seed := testStorageSeedSource(
		t,
		root,
		"/seed",
	)

	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "seed")
	if err := os.MkdirAll(replacement, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSeedFile(t, replacement, "state", "replacement")

	service := newStorageTestStore(t, base)
	home, err := service.ResolveHome(
		t.Context(),
		"exact-root",
		defaultProfile,
		seed,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	data, err := os.ReadFile(filepath.Join(home.HostPath(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "leased" {
		t.Fatalf("seed content = %q, want content from leased root descriptor", data)
	}
}

func TestSeedCopyUsesIndependentCursorsForSharedRootDescriptor(t *testing.T) {
	base := secureStorageTestPath(t)
	root := filepath.Join(base, "rootfs")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	const files = 64
	for index := range files {
		writeSeedFile(t, root, fmt.Sprintf("file-%02d", index), "data")
	}
	seed := testStorageSeedSource(
		t,
		root,
		"/",
	)

	service := newStorageTestStore(t, base)
	const publishers = 8
	start := make(chan struct{})
	errorsByPublisher := make(chan error, publishers)
	ctx := t.Context()
	var wait sync.WaitGroup
	for index := range publishers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start

			home, err := service.ResolveHome(
				ctx,
				fmt.Sprintf("root-cursor-%d", index),
				defaultProfile,
				seed,
			)
			if err != nil {
				errorsByPublisher <- err
				return
			}

			entries, readErr := os.ReadDir(home.HostPath())
			if readErr == nil && len(entries) != files {
				readErr = fmt.Errorf("seeded %d entries, want %d", len(entries), files)
			}
			errorsByPublisher <- errors.Join(readErr, home.Close())
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByPublisher)

	for err := range errorsByPublisher {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestSeedCopyCancellationRemovesPartialStage(t *testing.T) {
	base := secureStorageTestPath(t)
	root, directory := makeHomeSeedRoot(t, base)
	writeSeedFile(t, directory, "payload", strings.Repeat("x", 1<<20))

	service := newStorageTestStore(t, base)
	ctx := newCancelOnErrContext(5)
	_, err := service.ResolveHome(
		ctx,
		"cancelled",
		defaultProfile,
		testStorageSeedSource(
			t,
			root,
			"/seed",
		),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveHome error = %v, want context.Canceled", err)
	}
	assertHomeWasNotPublished(t, service, "cancelled")
}

func makeHomeSeedRoot(t *testing.T, base string) (string, string) {
	t.Helper()

	root := filepath.Join(base, "rootfs")
	directory := filepath.Join(root, "seed")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, directory
}

func writeSeedFile(t *testing.T, directory, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newStorageTestStoreWithLimits(t *testing.T, base string, limits Limits) *Store {
	t.Helper()

	service, err := NewStore(
		config.Paths{XDGDataHome: base},
		limits,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return service
}

func assertHomeWasNotPublished(t *testing.T, service *Store, displayName string) {
	t.Helper()

	identity, err := ResolveHomeIdentity(displayName, defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(service.volumes.Path(), identity.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home publication exists: %v", err)
	}

	entries, err := os.ReadDir(service.volumes.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".toby-tmp-") {
			t.Errorf("temporary publication remains: %s", entry.Name())
		}
	}
}

type cancelOnErrContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining atomic.Int64
}

func newCancelOnErrContext(checks int64) *cancelOnErrContext {
	ctx, cancel := context.WithCancel(context.Background())
	result := &cancelOnErrContext{
		Context: ctx,
		cancel:  cancel,
	}
	result.remaining.Store(checks)
	return result
}

func (c *cancelOnErrContext) Err() error {
	if c.remaining.Add(-1) == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
