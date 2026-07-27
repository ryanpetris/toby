//go:build linux

package runtimeassets

// Verifies exact Linux file capabilities, path-independent source authority,
// and bounded cleanup.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
)

func TestMaterializeProducesDeterministicExactSources(t *testing.T) {
	t.Parallel()

	data := []byte("#!/bin/sh\necho original\n")
	registry, err := NewRegistry([]Asset{
		{
			Target: layout.Runtime + "/z-install",
			Data:   data,
			Mode:   0o755,
		},
		{
			Target: layout.Runtime + "/a-config",
			Data:   []byte("configuration\n"),
			Mode:   0o400,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	copy(data, []byte("#!/bin/sh\necho changed!\n"))

	root, _ := openRuntimeAssetRoot(t)
	set, err := registry.Materialize(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	assets := set.RuntimeAssets()
	if len(assets) != 2 {
		t.Fatalf("RuntimeAssets() length = %d, want 2", len(assets))
	}
	if assets[0].Target != layout.Runtime+"/a-config" ||
		assets[1].Target != layout.Runtime+"/z-install" {
		t.Fatalf("RuntimeAssets() targets = %#v, want target-sorted", assets)
	}
	for index, asset := range assets {
		if asset.Access != mount.AccessReadOnly {
			t.Errorf("asset %d access = %q, want %q", index, asset.Access, mount.AccessReadOnly)
		}
		wantName := assetFileName(index)
		if filepath.Base(asset.HostPath) != wantName {
			t.Errorf(
				"asset %d host basename = %q, want %q",
				index,
				filepath.Base(asset.HostPath),
				wantName,
			)
		}
	}

	assets[0].Target = "/changed"
	if got := set.RuntimeAssets()[0].Target; got != layout.Runtime+"/a-config" {
		t.Fatalf("RuntimeAssets() leaked internal slice: %q", got)
	}

	sources, err := set.Sources()
	if err != nil {
		t.Fatal(err)
	}
	assertSource(t, sources[layout.Runtime+"/a-config"], "configuration\n", 0o400)
	assertSource(
		t,
		sources[layout.Runtime+"/z-install"],
		"#!/bin/sh\necho original\n",
		0o755,
	)

	delete(sources, layout.Runtime+"/a-config")
	sourcesAgain, err := set.Sources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sourcesAgain) != 2 {
		t.Fatalf("Sources() map length = %d after caller mutation, want 2", len(sourcesAgain))
	}
}

func TestMaterializeRetainsSourceAuthorityAndCleansStorage(t *testing.T) {
	t.Parallel()

	target := layout.Runtime + "/installer"
	registry, err := NewRegistry([]Asset{{
		Target: target,
		Data:   []byte("original"),
		Mode:   0o500,
	}})
	if err != nil {
		t.Fatal(err)
	}

	root, _ := openRuntimeAssetRoot(t)
	set, err := registry.Materialize(root, nil)
	if err != nil {
		t.Fatal(err)
	}

	assets := set.RuntimeAssets()
	sources, err := set.Sources()
	if err != nil {
		t.Fatal(err)
	}
	source := sources[target]
	storagePath := filepath.Dir(assets[0].HostPath)
	movedPath := assets[0].HostPath + ".moved"
	if err := os.Rename(assets[0].HostPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assets[0].HostPath, []byte("replacement"), 0o500); err != nil {
		t.Fatal(err)
	}

	assertSource(t, source, "original", 0o500)

	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialization storage still exists after Close: %v", err)
	}
	if _, err := source.Stat(); err == nil {
		t.Fatal("source descriptor remained open after Close")
	}
	if _, err := set.Sources(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Sources() after Close error = %v, want os.ErrClosed", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestMaterializeTransfersStorageCleanupToEnclosingRun(t *testing.T) {
	t.Parallel()

	target := layout.Runtime + "/installer"
	registry, err := NewRegistry([]Asset{{
		Target: target,
		Data:   []byte("installer"),
		Mode:   0o500,
	}})
	if err != nil {
		t.Fatal(err)
	}

	root, _ := openRuntimeAssetRoot(t)
	set, err := registry.Materialize(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	assets := set.RuntimeAssets()
	sources, err := set.Sources()
	if err != nil {
		t.Fatal(err)
	}
	source := sources[target]
	storagePath := filepath.Dir(assets[0].HostPath)

	if err := set.TransferStorageCleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("transferred materialization storage was removed: %v", err)
	}
	if _, err := source.Stat(); err == nil {
		t.Fatal("source descriptor remained open after storage transfer")
	}
	if _, err := set.Sources(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf(
			"Sources() after storage transfer error = %v, want os.ErrClosed",
			err,
		)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() after storage transfer error = %v", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("Close removed transferred materialization storage: %v", err)
	}
}

func TestMaterializeAcceptsAndPreservesExistingRootMode(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	root, rootPath := openRuntimeAssetRoot(t)
	if err := os.Chmod(rootPath, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(rootPath, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore runtime root mode: %v", err)
		}
	})

	set, err := registry.Materialize(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(rootPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o750 {
		t.Fatalf("runtime root mode = %04o, want preserved 0750", info.Mode().Perm())
	}
}

func openRuntimeAssetRoot(
	t *testing.T,
) (*safefs.Directory, string) {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(
		workingDirectory,
		".toby-runtimeassets-test-",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		os.RemoveAll(path)
		t.Fatal(err)
	}

	root, err := openRuntimeAssetsTestRoot(path)
	if err != nil {
		os.RemoveAll(path)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close runtime root: %v", err)
		}
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove runtime root: %v", err)
		}
	})

	return root, path
}

func openRuntimeAssetsTestRoot(
	path string,
) (*safefs.Directory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return safefs.OpenDirectoryFile(
		file,
		path,
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
		},
	)
}

func assertSource(
	t *testing.T,
	source *os.File,
	wantData string,
	wantMode os.FileMode,
) {
	t.Helper()
	if source == nil {
		t.Fatal("source is nil")
	}

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != wantData {
		t.Fatalf("source data = %q, want %q", data, wantData)
	}

	var status unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &status); err != nil {
		t.Fatal(err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("source type = %#o, want regular", status.Mode&unix.S_IFMT)
	}
	if status.Mode&0o7777 != uint32(wantMode.Perm()) {
		t.Fatalf(
			"source mode = %04o, want %04o",
			status.Mode&0o7777,
			wantMode.Perm(),
		)
	}
	if int(status.Uid) != os.Geteuid() || int(status.Gid) != os.Getegid() {
		t.Fatalf(
			"source identity = %d:%d, want %d:%d",
			status.Uid,
			status.Gid,
			os.Geteuid(),
			os.Getegid(),
		)
	}
}
