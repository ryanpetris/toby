package oci

// Verifies that deterministic build references reuse Toby's published rootfs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

func TestStoreBuildIfMissingBypassesBuildahForPublishedReference(
	t *testing.T,
) {
	store, request := publishBuildCacheFixture(t)
	t.Setenv("PATH", t.TempDir())

	prepared, err := store.Prepare(
		t.Context(),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreBuildAlwaysInvokesBuildahForPublishedReference(
	t *testing.T,
) {
	store, request := publishBuildCacheFixture(t)
	t.Setenv("PATH", t.TempDir())
	request.PullPolicy = image.PullAlways

	_, err := store.Prepare(t.Context(), request)
	if err == nil ||
		!strings.Contains(err.Error(), "buildah is required") {
		t.Fatalf("Prepare error = %v", err)
	}
}

func TestStoreBuildUsesCacheForIntermediateArchive(t *testing.T) {
	root := t.TempDir()
	buildahPath := filepath.Join(root, "buildah")
	script, err := os.ReadFile(filepath.Join("testdata", "buildah"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(buildahPath, script, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)

	argumentsPath := filepath.Join(root, "arguments")
	t.Setenv("TOBY_TEST_BUILDAH_ARGS", argumentsPath)

	paths := testPaths(t)
	store := openTestService(t, paths, &fakePipeline{})
	_, err = store.Prepare(t.Context(), Request{
		Source:    imagesource.Build,
		Reference: "toby.local/default/toby:test",
		Build: imagesource.BuildConfig{
			Context:    filepath.Join(root, "context"),
			Dockerfile: filepath.Join(root, "Dockerfile"),
		},
		Platform:   testPlatform(),
		PullPolicy: image.PullAlways,
	})
	if err == nil {
		t.Fatal("empty Buildah archive succeeded")
	}

	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	cachePrefix := "oci-archive:" + filepath.Join(
		paths.TobyCacheDir(),
		"images",
		"builds",
		"archive-",
	)
	if !strings.Contains(string(arguments), cachePrefix) {
		t.Fatalf(
			"Buildah arguments %q do not contain cache prefix %q",
			arguments,
			cachePrefix,
		)
	}
	if strings.Contains(
		string(arguments),
		filepath.Join(paths.TobyDataDir(), "images", "tmp"),
	) {
		t.Fatalf("Buildah archive uses data staging: %q", arguments)
	}

	entries, err := os.ReadDir(filepath.Join(
		paths.TobyCacheDir(),
		"images",
		"builds",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("intermediate archive entries remain: %#v", entries)
	}
}

func publishBuildCacheFixture(
	t *testing.T,
) (*Store, Request) {
	t.Helper()

	layoutPath := filepath.Join(t.TempDir(), "layout")
	if err := writeTestLayout(layoutPath, testPlatform()); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "image.tar")
	writeDirectoryArchive(t, archivePath, layoutPath)

	store := newTestStore(t, &fakePipeline{})
	const reference = "toby.local/default/cache-test:" +
		"0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef"
	prepared, err := store.Prepare(t.Context(), Request{
		Source:     imagesource.Archive,
		Reference:  reference,
		Archive:    archivePath,
		Platform:   testPlatform(),
		PullPolicy: image.PullAlways,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}

	return store, Request{
		Source:    imagesource.Build,
		Reference: reference,
		Build: imagesource.BuildConfig{
			Context:    filepath.Join(t.TempDir(), "context"),
			Dockerfile: filepath.Join(t.TempDir(), "Dockerfile"),
		},
		Platform:   testPlatform(),
		PullPolicy: image.PullIfMissing,
	}
}
