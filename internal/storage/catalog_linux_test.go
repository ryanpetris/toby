//go:build linux

package storage

// Exercises volume inventory, real paths, corruption visibility, and safe
// removal coordination.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
)

func TestVolumeCatalogListsAndInspectsRealPaths(t *testing.T) {
	base := secureStorageTestPath(t)
	dataHome := filepath.Join(base, "data")
	target := filepath.Join(base, "linked-data")
	if err := os.MkdirAll(dataHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
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
		"workspace",
		"personal",
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()

	volumes, err := service.ListVolumes(t.Context(), VolumeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 {
		t.Fatalf("volume count = %d, want 1", len(volumes))
	}

	got := volumes[0]
	if got.Type != VolumeTypeHome ||
		got.Name != "workspace" ||
		got.Profile != "personal" ||
		got.Purpose != "" ||
		got.Problem != "" {
		t.Fatalf("volume = %#v", got)
	}
	wantObject := filepath.Join(target, "volumes", got.ID)
	if got.ObjectPath != wantObject {
		t.Fatalf("object path = %q, want %q", got.ObjectPath, wantObject)
	}
	if got.DataPath != filepath.Join(wantObject, volumeDataDirectory) {
		t.Fatalf("data path = %q", got.DataPath)
	}
	if got.MetadataPath != filepath.Join(wantObject, volumeMetadataFile) {
		t.Fatalf("metadata path = %q", got.MetadataPath)
	}

	inspected, err := service.InspectVolume(
		t.Context(),
		got.ShortID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected != got {
		t.Fatalf("InspectVolume() = %#v, want %#v", inspected, got)
	}
}

func TestCreateVolumeIsIdempotentAndSupportsMetadataSelection(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	defer service.Close()

	spec := VolumeSpec{
		Type: VolumeTypeHome,
		Name: "migration",
	}
	created, err := service.CreateVolume(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if created.Type != VolumeTypeHome ||
		created.Name != "migration" ||
		created.Profile != defaultProfile ||
		created.Purpose != "" {
		t.Fatalf("created volume = %#v", created)
	}

	marker := filepath.Join(created.DataPath, "migrated")
	if err := os.WriteFile(marker, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}

	again, err := service.CreateVolume(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != created.ID {
		t.Fatalf("second create ID = %q, want %q", again.ID, created.ID)
	}
	if data, err := os.ReadFile(marker); err != nil ||
		string(data) != "state" {
		t.Fatalf("migrated marker = %q, %v", data, err)
	}

	selected, err := service.InspectVolumeBySpec(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != created.ID {
		t.Fatalf("selected ID = %q, want %q", selected.ID, created.ID)
	}

	home, err := service.ResolveHome(
		t.Context(),
		"migration",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	if home.Identity().ID != created.ID {
		t.Fatalf(
			"resolved home ID = %q, want %q",
			home.Identity().ID,
			created.ID,
		)
	}
}

func TestListVolumesFiltersEveryMetadataField(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	defer service.Close()

	specs := []VolumeSpec{
		{Type: VolumeTypeHome, Name: "workspace", Profile: "work"},
		{Type: VolumeTypeHome, Name: "workspace", Profile: "personal"},
		{
			Type:    VolumeTypeTool,
			Name:    "opencode",
			Profile: "work",
			Purpose: "config",
		},
		{
			Type:    VolumeTypeTool,
			Name:    "opencode",
			Profile: "work",
			Purpose: "data",
		},
	}
	for _, spec := range specs {
		if _, err := service.CreateVolume(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
	}

	volumes, err := service.ListVolumes(t.Context(), VolumeFilter{
		Type:    VolumeTypeTool,
		Name:    "opencode",
		Profile: "work",
		Purpose: "data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 ||
		volumes[0].Type != VolumeTypeTool ||
		volumes[0].Purpose != "data" {
		t.Fatalf("filtered volumes = %#v", volumes)
	}

	homes, err := service.ListVolumes(t.Context(), VolumeFilter{
		Type: VolumeTypeHome,
		Name: "workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(homes) != 2 {
		t.Fatalf("home count = %d, want 2", len(homes))
	}
}

func TestRemoveVolumeRefusesLiveHandle(t *testing.T) {
	base := secureStorageTestPath(t)
	first := newStorageTestStore(t, base)
	second := newStorageTestStore(t, base)

	home, err := first.ResolveHome(
		t.Context(),
		"active",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := home.Identity().ID

	if _, err := second.RemoveVolumes(
		t.Context(),
		[]string{id[:volumeIDShortLength]},
		nil,
	); !errors.Is(err, ErrVolumeBusy) {
		t.Fatalf("RemoveVolumes() error = %v, want ErrVolumeBusy", err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}

	removed, err := second.RemoveVolumes(
		t.Context(),
		[]string{id[:volumeIDShortLength]},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != id {
		t.Fatalf("removed IDs = %#v, want %q", removed, id)
	}
	if _, err := os.Lstat(filepath.Join(second.volumes.Path(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed volume still exists: %v", err)
	}
}

func TestRemoveVolumesChecksEveryLeaseBeforeDeleting(t *testing.T) {
	base := secureStorageTestPath(t)
	first := newStorageTestStore(t, base)
	second := newStorageTestStore(t, base)

	inactive, err := first.CreateVolume(t.Context(), VolumeSpec{
		Type: VolumeTypeHome,
		Name: "inactive",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := first.ResolveHome(
		t.Context(),
		"active",
		defaultProfile,
		SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()

	var progress []VolumeRemovalProgress
	if _, err := second.RemoveVolumes(
		t.Context(),
		[]string{inactive.ID, active.Identity().ID},
		func(event VolumeRemovalProgress) {
			progress = append(progress, event)
		},
	); !errors.Is(err, ErrVolumeBusy) {
		t.Fatalf("RemoveVolumes() error = %v, want ErrVolumeBusy", err)
	}
	if len(progress) != 0 {
		t.Fatalf("progress before lease preflight completed = %#v", progress)
	}
	if _, err := os.Stat(
		filepath.Join(second.volumes.Path(), inactive.ID),
	); err != nil {
		t.Fatalf("inactive volume was partially removed: %v", err)
	}
}

func TestRemoveVolumesReportsEachCompletedDeletion(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	defer service.Close()

	first, err := service.CreateVolume(t.Context(), VolumeSpec{
		Type: VolumeTypeHome,
		Name: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateVolume(t.Context(), VolumeSpec{
		Type:    VolumeTypeTool,
		Name:    "opencode",
		Purpose: "config",
	})
	if err != nil {
		t.Fatal(err)
	}

	var progress []VolumeRemovalProgress
	removed, err := service.RemoveVolumes(
		t.Context(),
		[]string{first.ID, second.ID},
		func(event VolumeRemovalProgress) {
			progress = append(progress, event)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 ||
		removed[0] != first.ID ||
		removed[1] != second.ID {
		t.Fatalf("removed IDs = %#v", removed)
	}

	want := []VolumeRemovalProgress{
		{ID: first.ID, Phase: VolumeRemovalPhaseRemoving},
		{ID: first.ID, Phase: VolumeRemovalPhaseRemoved},
		{ID: second.ID, Phase: VolumeRemovalPhaseRemoving},
		{ID: second.ID, Phase: VolumeRemovalPhaseRemoved},
	}
	if len(progress) != len(want) {
		t.Fatalf("progress = %#v, want %#v", progress, want)
	}
	for index := range want {
		if progress[index] != want[index] {
			t.Fatalf("progress = %#v, want %#v", progress, want)
		}
	}
}

func TestRemoveVolumesDeletesReadOnlyApplicationData(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	defer service.Close()

	volume, err := service.CreateVolume(t.Context(), VolumeSpec{
		Type: VolumeTypeHome,
		Name: "toby",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Go writes its module cache read-only, so both the cached files and the
	// directories holding them deny the write access an unlink needs.
	module := filepath.Join(volume.DataPath, "go", "pkg", "mod", "reference@v0.6.0")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(module, ".gitattributes"),
		[]byte("cached"),
		0o444,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(module, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(module, 0o700)
	})

	removed, err := service.RemoveVolumes(t.Context(), []string{volume.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != volume.ID {
		t.Fatalf("removed IDs = %#v, want %q", removed, volume.ID)
	}
	if _, err := os.Lstat(volume.ObjectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("volume remains: %v", err)
	}
}

func TestRemoveVolumesStopsBetweenVolumesWhenCancelled(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	defer service.Close()

	first, err := service.CreateVolume(t.Context(), VolumeSpec{
		Type: VolumeTypeHome,
		Name: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateVolume(t.Context(), VolumeSpec{
		Type: VolumeTypeHome,
		Name: "second",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	removed, err := service.RemoveVolumes(
		ctx,
		[]string{first.ID, second.ID},
		func(event VolumeRemovalProgress) {
			if event.ID == first.ID &&
				event.Phase == VolumeRemovalPhaseRemoved {
				cancel()
			}
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveVolumes() error = %v, want context canceled", err)
	}
	if len(removed) != 1 || removed[0] != first.ID {
		t.Fatalf("removed IDs = %#v, want first volume", removed)
	}
	if _, err := os.Stat(
		filepath.Join(service.volumes.Path(), second.ID),
	); err != nil {
		t.Fatalf("second volume was removed after cancellation: %v", err)
	}
}

func TestMalformedVolumeRemainsVisibleAndRemovable(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)
	id := strings.Repeat("a", 128)
	object := filepath.Join(service.volumes.Path(), id)
	if err := os.MkdirAll(
		filepath.Join(object, volumeDataDirectory),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(object, volumeMetadataFile),
		[]byte("not json\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	volumes, err := service.ListVolumes(t.Context(), VolumeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || volumes[0].ID != id || volumes[0].Problem == "" {
		t.Fatalf("volumes = %#v", volumes)
	}

	inspected, err := service.InspectVolume(t.Context(), id)
	if err == nil || inspected.ID != id || inspected.Problem == "" {
		t.Fatalf("InspectVolume() = %#v, %v", inspected, err)
	}

	if _, err := service.RemoveVolumes(
		t.Context(),
		[]string{id},
		nil,
	); err != nil {
		t.Fatal(err)
	}
}
