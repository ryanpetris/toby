package ociresource

// Covers canonical defaults shared by agent OCI resource identities.

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

func TestNormalizeCanonicalizesDefaultsReferenceAndPlatform(t *testing.T) {
	config, err := Normalize(Config{
		Reference: " alpine ",
		Platform: ocispec.Platform{
			OS:           " linux ",
			Architecture: " " + runtime.GOARCH + " ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Reference != "docker.io/library/alpine:latest" ||
		config.Platform.OS != "linux" ||
		config.Platform.Architecture != runtime.GOARCH ||
		config.PullPolicy != image.PullIfMissing {
		t.Fatalf("config = %#v", config)
	}
}

func TestNormalizeRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := Normalize(Config{
		Reference: "alpine",
		Platform: ocispec.Platform{
			OS:           "darwin",
			Architecture: "amd64",
		},
	}); err == nil {
		t.Fatal("unsupported platform succeeded")
	}
}

func TestNormalizeCanonicalizesArchiveSource(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "image.tar")
	config, err := Normalize(Config{
		Source:  imagesource.Archive,
		Archive: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Archive != archive ||
		config.PullPolicy != image.PullIfMissing ||
		!strings.HasPrefix(
			config.Reference,
			"toby.local/archive/",
		) {
		t.Fatalf("config = %#v", config)
	}

	again, err := Normalize(Config{
		Source:  imagesource.Archive,
		Archive: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Reference != config.Reference {
		t.Fatalf(
			"derived references differ: %q and %q",
			config.Reference,
			again.Reference,
		)
	}
}

func TestNormalizeCanonicalizesBuildSource(t *testing.T) {
	root := t.TempDir()
	contextPath := filepath.Join(root, ".")
	dockerfile := filepath.Join(root, "Dockerfile")
	config, err := Normalize(Config{
		Source: imagesource.Build,
		Build: imagesource.BuildConfig{
			Context:    contextPath,
			Dockerfile: dockerfile,
		},
		Profile: "default",
		Project: "toby",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Build.Context != filepath.Clean(root) ||
		config.Build.Dockerfile != dockerfile ||
		config.PullPolicy != image.PullIfMissing ||
		!strings.HasPrefix(
			config.Reference,
			"toby.local/default/toby:",
		) {
		t.Fatalf("config = %#v", config)
	}
	tag, found := strings.CutPrefix(
		config.Reference,
		"toby.local/default/toby:",
	)
	if !found || len(tag) != 64 || tag == strings.Repeat("0", 64) {
		t.Fatalf("build reference tag = %q", tag)
	}

	again, err := Normalize(Config{
		Source: imagesource.Build,
		Build: imagesource.BuildConfig{
			Context:    contextPath,
			Dockerfile: dockerfile,
		},
		Profile: "default",
		Project: "toby",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Reference != config.Reference {
		t.Fatalf(
			"derived references differ: %q and %q",
			config.Reference,
			again.Reference,
		)
	}
}

func TestNormalizePreservesBuildPullPolicy(t *testing.T) {
	root := t.TempDir()
	config, err := Normalize(Config{
		Source: imagesource.Build,
		Build: imagesource.BuildConfig{
			Context:    root,
			Dockerfile: filepath.Join(root, "Dockerfile"),
		},
		Profile:    "default",
		Project:    "toby",
		PullPolicy: image.PullAlways,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.PullPolicy != image.PullAlways {
		t.Fatalf("pull policy = %q", config.PullPolicy)
	}
}

func TestNormalizeEncodesBuildNamesForOCIRepository(t *testing.T) {
	root := t.TempDir()
	config, err := Normalize(Config{
		Source: imagesource.Build,
		Build: imagesource.BuildConfig{
			Context:    root,
			Dockerfile: filepath.Join(root, "Dockerfile"),
		},
		Profile: "Review Work",
		Project: "日本語",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(
		config.Reference,
		"toby.local/review-work-",
	) || !strings.Contains(config.Reference, "/name-") {
		t.Fatalf("encoded build reference = %q", config.Reference)
	}
}

func TestNormalizeRejectsBuildWithoutProfileOrProject(t *testing.T) {
	root := t.TempDir()
	for name, config := range map[string]Config{
		"profile": {
			Project: "toby",
		},
		"project": {
			Profile: "default",
		},
	} {
		t.Run(name, func(t *testing.T) {
			config.Source = imagesource.Build
			config.Build = imagesource.BuildConfig{
				Context:    root,
				Dockerfile: filepath.Join(root, "Dockerfile"),
			}
			if _, err := Normalize(config); err == nil {
				t.Fatalf("build without %s succeeded", name)
			}
		})
	}
}

func TestNormalizeRejectsRelativeLocalSourcePaths(t *testing.T) {
	for name, config := range map[string]Config{
		"archive": {
			Source:  imagesource.Archive,
			Archive: "image.tar",
		},
		"build context": {
			Source: imagesource.Build,
			Build: imagesource.BuildConfig{
				Context:    ".",
				Dockerfile: "/tmp/Dockerfile",
			},
			Profile: "default",
			Project: "toby",
		},
		"Dockerfile": {
			Source: imagesource.Build,
			Build: imagesource.BuildConfig{
				Context:    "/tmp/context",
				Dockerfile: "Dockerfile",
			},
			Profile: "default",
			Project: "toby",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(config); err == nil {
				t.Fatal("relative local source path succeeded")
			}
		})
	}
}
