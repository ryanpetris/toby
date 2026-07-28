//go:build linux

package run

// Verifies the ownership and teardown boundary between preallocated run
// directories and retained storage.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage"
)

func TestNativeRunRequiresProtectedRootsWithoutConsumingInput(t *testing.T) {
	tests := []struct {
		name  string
		clear func(*bwrap.ProtectedRoots)
		match string
	}{
		{
			name: "image store",
			clear: func(roots *bwrap.ProtectedRoots) {
				roots.ImageStore = nil
			},
			match: "OCI image-store root descriptor is required",
		},
		{
			name: "persistent data",
			clear: func(roots *bwrap.ProtectedRoots) {
				roots.PersistentData = nil
			},
			match: "Toby persistent-data root descriptor is required",
		},
		{
			name: "run storage",
			clear: func(roots *bwrap.ProtectedRoots) {
				roots.RunStorage = nil
			},
			match: "Bubblewrap run-storage root descriptor is required",
		},
		{
			name: "runtime",
			clear: func(roots *bwrap.ProtectedRoots) {
				roots.Runtime = nil
			},
			match: "Toby runtime root descriptor is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, directories, runRoot := nativeOwnershipInput(t)
			test.clear(&input.ProtectedRoots)

			_, err := NewNativeRun(t.Context(), input)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("NewNativeRun error = %v, want %q", err, test.match)
			}
			if _, err := os.Stat(runRoot); err != nil {
				t.Fatalf(
					"protected-root validation failure consumed run directories: %v",
					err,
				)
			}
			if err := directories.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeRunConstructionFailureRevokesRunDirectories(
	t *testing.T,
) {
	input, _, runRoot := nativeOwnershipInput(t)
	if err := input.SandboxBinary.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := NewNativeRun(t.Context(), input)
	if err == nil {
		t.Fatal("NewNativeRun succeeded with a closed sandbox helper")
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("construction failure left run directories: %v", err)
	}
	if input.Prepared.(*nativeTestImage).closed {
		t.Fatal("construction failure consumed caller-owned image lease")
	}
	if input.Home.(*nativeTestHome).closed {
		t.Fatal("construction failure consumed caller-owned home lease")
	}
}

func TestNativeRunCloseCompletesBoundedOverlayCleanup(
	t *testing.T,
) {
	input, _, runRoot := nativeOwnershipInputWithLimits(
		t,
		bwrap.RunStorageLimits{MaxCleanupEntries: 4},
	)

	managedPath := filepath.Join(
		input.ProtectedRoots.PersistentData.Name(),
		"volumes",
		"tool-id",
		"_data",
	)
	if err := os.MkdirAll(managedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	managed := &nativeTestManaged{
		nativeTestDirectory: &nativeTestDirectory{path: managedPath},
		entry: mount.Entry{
			Key: mount.Key{
				Type:    mount.TypeTool,
				Name:    "agent",
				Purpose: "state",
			},
			Profile:  "default",
			HostPath: managedPath,
			Target:   layout.Home + "/.agent",
			Access:   mount.AccessRegular,
		},
	}
	input.Managed = []ManagedDirectory{managed}
	if err := input.ToolSandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "agent",
			Purpose: "state",
		},
		Target: "~/.agent",
	}); err != nil {
		t.Fatal(err)
	}

	native, err := NewNativeRun(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		name := filepath.Join(runRoot, "upper", fmt.Sprintf("entry-%d", index))
		if err := os.WriteFile(name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	if !input.Prepared.(*nativeTestImage).closed ||
		!input.Home.(*nativeTestHome).closed ||
		!managed.closed {
		t.Fatal("Close retained storage leases")
	}
	if native.bubblewrapRun() != nil {
		t.Fatal("Close retained the Bubblewrap run")
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run overlay remains after Close: %v", err)
	}
}

func nativeOwnershipInput(
	t *testing.T,
) (NativeRunInput, *bwrap.RunDirectories, string) {
	t.Helper()

	return nativeOwnershipInputWithLimits(t, bwrap.RunStorageLimits{})
}

func nativeOwnershipInputWithLimits(
	t *testing.T,
	limits bwrap.RunStorageLimits,
) (NativeRunInput, *bwrap.RunDirectories, string) {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(
		workingDirectory,
		".toby-native-run-ownership-test-",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})

	persistentDataPath := filepath.Join(base, "data")
	imageStorePath := filepath.Join(persistentDataPath, "images")
	rootfsPath := filepath.Join(imageStorePath, "rootfs", "selected")
	homePath := filepath.Join(
		persistentDataPath,
		"volumes",
		"home-id",
		"_data",
	)
	runtimePath := filepath.Join(base, "runtime", "toby")
	for _, path := range []string{rootfsPath, homePath, runtimePath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	image := &nativeTestImage{
		nativeTestDirectory: &nativeTestDirectory{path: rootfsPath},
		spec: oci.Spec{
			Manifest: ocispec.Descriptor{
				Digest: digest.Digest(
					"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				),
			},
			Runtime: oci.RuntimeConfig{
				Environment: []string{"PATH=/usr/bin:/bin"},
			},
		},
	}
	home := &nativeTestHome{
		nativeTestDirectory: &nativeTestDirectory{path: homePath},
		identity: storage.HomeIdentity{
			DisplayName: "lease-test",
			ID:          "lease-test-a1b2c3",
			Profile:     "default",
		},
	}
	t.Cleanup(func() {
		if !image.closed {
			_ = image.Close()
		}
		if !home.closed {
			_ = home.Close()
		}
	})

	runStorage, err := bwrap.OpenRunStorage(
		filepath.Join(base, "runs"),
		limits,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runStorage.Close()
	})
	directories, err := runStorage.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Dir(directories.Overlay().Upper)
	t.Cleanup(func() {
		_ = directories.Close()
	})

	executable := filepath.Join(base, "toby")
	if err := os.WriteFile(executable, []byte("test executable"), 0o500); err != nil {
		t.Fatal(err)
	}
	tobyBinary, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tobyBinary.Close()
	})

	toolSandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{
		ImageEnvironment: image.spec.Runtime.Environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	protectedRoots := nativeTestProtectedRoots(
		t,
		imageStorePath,
		persistentDataPath,
		runtimePath,
		runStorage,
	)

	return NativeRunInput{
		Prepared:          image,
		Home:              home,
		Directories:       directories,
		ProtectedRoots:    protectedRoots,
		SandboxBinaryPath: executable,
		SandboxBinary:     tobyBinary,
		Workdir:           "/",
		Identity: bwrap.Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
		ToolSandbox: toolSandbox,
		Executor:    &nativeRecordingExecutor{},
	}, directories, runRoot
}
