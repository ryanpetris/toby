//go:build linux

package storage

// Exercises global profile selection, atomic tool-volume publication, and
// direct native sharing without application-wide locks.

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestToolVolumesAreGlobalWithinProfile(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)

	firstHome, err := service.ResolveHome(
		t.Context(),
		"first",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer firstHome.Close()
	secondHome, err := service.ResolveHome(
		t.Context(),
		"second",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer secondHome.Close()

	request := testManagedRequest()
	first := resolveOneManaged(
		t,
		service,
		ProfileSelection{Default: "default"},
		request,
		SeedSource{},
	)
	defer first.Close()
	second := resolveOneManaged(
		t,
		service,
		ProfileSelection{Default: "default"},
		request,
		SeedSource{},
	)
	defer second.Close()

	if first.Entry().HostPath != second.Entry().HostPath {
		t.Fatalf(
			"global tool paths differ: %q and %q",
			first.Entry().HostPath,
			second.Entry().HostPath,
		)
	}
	if first.Entry().Profile != defaultProfile {
		t.Fatalf("profile = %q, want %q", first.Entry().Profile, defaultProfile)
	}
	if firstHome.HostPath() == secondHome.HostPath() {
		t.Fatal("different home names resolved the same home volume")
	}
}

func TestToolVolumeProfilesSeparateStateAndSupportOverrides(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	request := testManagedRequest()

	work := resolveOneManaged(
		t,
		service,
		ProfileSelection{Default: "work"},
		request,
		SeedSource{},
	)
	defer work.Close()
	personal := resolveOneManaged(
		t,
		service,
		ProfileSelection{Default: "personal"},
		request,
		SeedSource{},
	)
	defer personal.Close()
	override := resolveOneManaged(
		t,
		service,
		ProfileSelection{
			Default: "work",
			Tools:   map[string]string{"opencode": "personal"},
		},
		request,
		SeedSource{},
	)
	defer override.Close()

	if work.Entry().HostPath == personal.Entry().HostPath {
		t.Fatal("different profiles resolved the same tool volume")
	}
	if override.Entry().HostPath != personal.Entry().HostPath {
		t.Fatal("per-tool profile override did not select the global personal volume")
	}
	if override.Entry().Profile != "personal" {
		t.Fatalf("override profile = %q, want personal", override.Entry().Profile)
	}
}

func TestToolVolumeUsesHashedDockerStyleLayout(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	request := testManagedRequest()
	handle := resolveOneManaged(
		t,
		service,
		ProfileSelection{},
		request,
		SeedSource{},
	)
	defer handle.Close()

	metadata := newToolMetadata(defaultProfile, "opencode", "data")
	id, wantMetadata, err := volumeID(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		t.Fatal(err)
	}
	wantData := filepath.Join(service.volumes.Path(), id, volumeDataDirectory)
	if handle.Entry().HostPath != wantData {
		t.Fatalf("tool volume path = %q, want %q", handle.Entry().HostPath, wantData)
	}
	gotMetadata, err := os.ReadFile(
		filepath.Join(service.volumes.Path(), id, volumeMetadataFile),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotMetadata) != string(wantMetadata) {
		t.Fatalf("metadata = %q, want %q", gotMetadata, wantMetadata)
	}
}

func TestToolVolumeSeedsOnlyOnCreation(t *testing.T) {
	base := secureStorageTestPath(t)
	firstRoot := filepath.Join(base, "first-root")
	secondRoot := filepath.Join(base, "second-root")
	if err := os.MkdirAll(filepath.Join(firstRoot, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(secondRoot, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "state", "value"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "state", "value"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := newStorageTestStore(t, base)
	request := testManagedRequest()
	request.Seed.ImagePath = "/state"
	first := resolveOneManaged(
		t,
		service,
		ProfileSelection{},
		request,
		testStorageSeedSource(t, firstRoot, "/"),
	)
	defer first.Close()
	again := resolveOneManaged(
		t,
		service,
		ProfileSelection{},
		request,
		testStorageSeedSource(t, secondRoot, "/"),
	)
	defer again.Close()

	data, err := os.ReadFile(filepath.Join(again.Entry().HostPath, "value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("existing tool volume was reseeded: %q", data)
	}
	if first.Entry().HostPath != again.Entry().HostPath {
		t.Fatal("seed source changed tool volume identity")
	}
}

func TestConcurrentToolVolumePublicationIsComplete(t *testing.T) {
	base := secureStorageTestPath(t)
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", "complete"), []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}

	services := []*Store{
		newStorageTestStore(t, base),
		newStorageTestStore(t, base),
	}
	request := testManagedRequest()
	request.Seed.ImagePath = "/state"
	seed := testStorageSeedSource(t, root, "/")

	const callers = 16
	start := make(chan struct{})
	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			handles, err := services[index%len(services)].ResolveManaged(
				t.Context(),
				ProfileSelection{},
				[]mount.Request{request},
				nil,
				seed,
			)
			if err != nil {
				errs <- err
				return
			}
			defer handles[0].Close()
			data, err := os.ReadFile(
				filepath.Join(handles[0].Entry().HostPath, "complete"),
			)
			if err != nil || string(data) != "yes" {
				errs <- errors.Join(err, errors.New("published tool volume is incomplete"))
				return
			}
			results <- handles[0].Entry().HostPath
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Error(err)
	}
	var expected string
	for path := range results {
		if expected == "" {
			expected = path
		}
		if path != expected {
			t.Fatalf("published paths differ: %q and %q", path, expected)
		}
	}
}

func TestToolVolumeResolutionDoesNotWaitForApplicationLocks(t *testing.T) {
	base := secureStorageTestPath(t)
	firstService := newStorageTestStore(t, base)
	secondService := newStorageTestStore(t, base)
	request := testManagedRequest()

	first := resolveOneManaged(
		t,
		firstService,
		ProfileSelection{},
		request,
		SeedSource{},
	)
	defer first.Close()
	lockPath := filepath.Join(first.Entry().HostPath, "application.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)

	second := resolveOneManaged(
		t,
		secondService,
		ProfileSelection{},
		request,
		SeedSource{},
	)
	defer second.Close()
	if first.Entry().HostPath != second.Entry().HostPath {
		t.Fatal("application lock changed volume resolution")
	}
}

func resolveOneManaged(
	t *testing.T,
	service *Store,
	profiles ProfileSelection,
	request mount.Request,
	seed SeedSource,
) *ManagedHandle {
	t.Helper()

	handles, err := service.ResolveManaged(
		t.Context(),
		profiles,
		[]mount.Request{request},
		nil,
		seed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 1 {
		t.Fatalf("managed handle count = %d, want 1", len(handles))
	}
	return handles[0]
}

func testManagedRequest() mount.Request {
	return mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "data",
		},
		Target: "~/.local/share/opencode",
	}
}
