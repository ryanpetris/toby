//go:build linux

package storage

// Exercises atomic first use, direct native inode sharing, seed safety, and
// absence of application-wide storage locks on Linux.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestNewServiceFollowsSymlinkedTobyDataRoot(t *testing.T) {
	base := secureStorageTestPath(t)
	dataHome := filepath.Join(base, "data")
	target := filepath.Join(base, "persistent-target")
	if err := os.MkdirAll(dataHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dataHome, "toby")); err != nil {
		t.Fatal(err)
	}

	service, err := NewStore(
		config.Paths{XDGDataHome: dataHome},
		DefaultLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	home, err := service.ResolveHome(
		t.Context(),
		"linked",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	got, err := os.Stat(filepath.Dir(filepath.Dir(home.HostPath())))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(filepath.Join(target, "volumes"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(got, want) {
		t.Fatal("private-home root does not resolve through the configured symlink")
	}
	if info, err := os.Stat(dataHome); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o750 {
		t.Fatalf("XDG data parent mode = %04o, want 0750", info.Mode().Perm())
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("resolved Toby data root mode = %04o, want 0700", info.Mode().Perm())
	}
	if info, err := os.Lstat(filepath.Join(dataHome, "toby")); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Toby data-root symlink was replaced")
	}
}

func TestHomeVolumeProfilesSeparateState(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)

	work, err := service.ResolveHome(
		t.Context(),
		"shared",
		"work",
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	personal, err := service.ResolveHome(
		t.Context(),
		"shared",
		"personal",
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()
	workAgain, err := service.ResolveHome(
		t.Context(),
		"shared",
		"work",
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer workAgain.Close()

	if work.HostPath() == personal.HostPath() {
		t.Fatal("different profiles resolved the same home volume")
	}
	if work.HostPath() != workAgain.HostPath() {
		t.Fatal("matching home name and profile did not reuse one volume")
	}
	if work.Identity().Profile != "work" ||
		personal.Identity().Profile != "personal" {
		t.Fatalf(
			"home profiles = work:%q personal:%q",
			work.Identity().Profile,
			personal.Identity().Profile,
		)
	}
}

func TestServicePublishesHomeOnceAndNeverReseeds(t *testing.T) {
	base := secureStorageTestPath(t)
	rootfs := filepath.Join(base, "rootfs")
	seedDirectory := filepath.Join(rootfs, "home", "developer")
	if err := os.MkdirAll(seedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(seedDirectory, "state.db")
	if err := os.WriteFile(source, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(seedDirectory, "state-link.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("state.db", filepath.Join(seedDirectory, "current")); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	seed := testStorageSeedSource(
		t,
		rootfs,
		"/home/developer",
	)
	home, err := service.ResolveHome(
		t.Context(),
		"work",
		defaultProfile,
		seed,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	got, err := os.ReadFile(filepath.Join(home.HostPath(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("seeded content = %q", got)
	}
	firstInfo, err := os.Stat(filepath.Join(home.HostPath(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Stat(filepath.Join(home.HostPath(), "state-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, linkInfo) {
		t.Fatal("seed hardlink group was not preserved")
	}
	if info, err := os.Lstat(filepath.Join(home.HostPath(), "current")); err != nil ||
		info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("safe relative symlink was not preserved: %v, %v", info, err)
	}

	if err := os.WriteFile(source, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := service.ResolveHome(
		t.Context(),
		"work",
		defaultProfile,
		SeedSource{
			ImagePath: "/home/developer",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	got, err = os.ReadFile(filepath.Join(again.HostPath(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("existing home was reseeded: %q", got)
	}
	if home.HostPath() != again.HostPath() {
		t.Fatalf("home paths differ: %q and %q", home.HostPath(), again.HostPath())
	}
}

func TestServiceSeedsFromWritableImmutableImageRoot(t *testing.T) {
	base := secureStorageTestPath(t)
	rootfs := filepath.Join(base, "rootfs")
	seedDirectory := filepath.Join(rootfs, "home", "developer")
	if err := os.MkdirAll(seedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDirectory, "state"), []byte("seeded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rootfs, 0o777); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	home, err := service.ResolveHome(
		t.Context(),
		"writable-root",
		defaultProfile,
		testStorageSeedSource(
			t,
			rootfs,
			"/home/developer",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	data, err := os.ReadFile(filepath.Join(home.HostPath(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "seeded" {
		t.Fatalf("seeded content = %q, want seeded", data)
	}
}

func TestServiceConcurrentFirstUsePublishesOneCompleteHome(t *testing.T) {
	base := secureStorageTestPath(t)
	rootfs := filepath.Join(base, "rootfs")
	seedDirectory := filepath.Join(rootfs, "home", "developer")
	if err := os.MkdirAll(seedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDirectory, "complete"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	seed := testStorageSeedSource(
		t,
		rootfs,
		"/home/developer",
	)

	const callers = 16
	paths := make(chan string, callers)
	errs := make(chan error, callers)
	ctx := t.Context()
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			home, err := service.ResolveHome(
				ctx,
				"race",
				defaultProfile,
				seed,
			)
			if err != nil {
				errs <- err
				return
			}
			defer home.Close()
			if data, err := os.ReadFile(filepath.Join(home.HostPath(), "complete")); err != nil ||
				string(data) != "yes" {
				errs <- errors.Join(err, errors.New("published home is incomplete"))
				return
			}
			paths <- home.HostPath()
		}()
	}
	wait.Wait()
	close(errs)
	close(paths)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var expected string
	for value := range paths {
		if expected == "" {
			expected = value
		}
		if value != expected {
			t.Fatalf("resolved paths differ: %q and %q", value, expected)
		}
	}
}

func TestServiceManagedDirectoriesShareNativeInodesAndLocks(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	home, err := service.ResolveHome(
		t.Context(),
		"shared",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	request := mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "data"},
		Target: "~/.local/share/opencode",
	}
	first, err := service.ResolveManaged(
		t.Context(),
		ProfileSelection{},
		[]mount.Request{request},
		nil,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first[0].Close()
	second, err := service.ResolveManaged(
		t.Context(),
		ProfileSelection{},
		[]mount.Request{request},
		nil,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second[0].Close()

	firstInfo, err := os.Stat(first[0].Entry().HostPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second[0].Entry().HostPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatal("managed resolutions do not expose the same native directory inode")
	}

	database := filepath.Join(first[0].Entry().HostPath, "opencode.db")
	one, err := os.OpenFile(database, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := os.OpenFile(database, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()

	if err := unix.Flock(int(one.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(two.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("second native lock error = %v, want EWOULDBLOCK", err)
	}
	if err := unix.Flock(int(one.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(two.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("second lock did not succeed after release: %v", err)
	}
}

func TestServiceDoesNotRepairCorruptPublishedHome(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	identity, err := ResolveHomeIdentity("corrupt", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	object, err := service.volumes.MkdirAll(identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, err := object.MkdirAll(volumeDataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	home.Close()
	object.Close()

	if _, err := service.ResolveHome(
		t.Context(),
		"corrupt",
		defaultProfile,
		SeedSource{},
	); err == nil {
		t.Fatal("corrupt published home was silently repaired")
	}
}

func TestServiceRepairsVolumeObjectAndPreservesExistingHomeMode(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)

	home, err := service.ResolveHome(
		t.Context(),
		"non-private",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	hostPath := home.HostPath()
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hostPath, 0o777); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Dir(hostPath)
	if err := os.Chmod(objectPath, 0o755); err != nil {
		t.Fatal(err)
	}

	reopened, err := service.ResolveHome(
		t.Context(),
		"non-private",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	info, err := os.Stat(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("private-home mode = %04o, want preserved 0777", info.Mode().Perm())
	}
	objectInfo, err := os.Stat(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if objectInfo.Mode().Perm() != 0o700 {
		t.Fatalf("volume object mode = %04o, want repaired 0700", objectInfo.Mode().Perm())
	}
}

func TestServiceRejectsEscapingSeedSymlinkWithoutPublishing(t *testing.T) {
	base := secureStorageTestPath(t)
	rootfs := filepath.Join(base, "rootfs")
	seedDirectory := filepath.Join(rootfs, "home", "developer")
	if err := os.MkdirAll(seedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(seedDirectory, "escape")); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	_, err := service.ResolveHome(
		t.Context(),
		"unsafe",
		defaultProfile,
		testStorageSeedSource(
			t,
			rootfs,
			"/home/developer",
		),
	)
	if !errors.Is(err, ErrUnsupportedSeed) {
		t.Fatalf("ResolveHome error = %v, want ErrUnsupportedSeed", err)
	}

	identity, err := ResolveHomeIdentity("unsafe", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(service.volumes.Path(), identity.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe home publication exists: %v", err)
	}
}

func newStorageTestStore(t *testing.T, base string) *Store {
	t.Helper()

	service, err := NewStore(
		config.Paths{XDGDataHome: base},
		DefaultLimits(),
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

func secureStorageTestPath(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp(".", ".toby-storage-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Errorf("remove test directory: %v", err)
		}
	})
	return absolute
}

func testStorageSeedSource(
	t *testing.T,
	root string,
	imagePath string,
) SeedSource {
	t.Helper()

	file, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close seed root: %v", err)
		}
	})
	return SeedSource{
		Root:            file,
		RootDescription: root,
		ImagePath:       imagePath,
	}
}
