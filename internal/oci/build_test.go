package oci

// Exercises Buildah discovery, arguments, output, and OCI archive export.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/imagesource"
)

func TestBuildArchiveRunsBuildahWithLayerCaching(t *testing.T) {
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

	contextPath := filepath.Join(root, "context")
	if err := os.Mkdir(contextPath, 0o700); err != nil {
		t.Fatal(err)
	}
	dockerfilePath := filepath.Join(contextPath, "Tobyfile")
	if err := os.WriteFile(
		dockerfilePath,
		[]byte("FROM scratch\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "image.tar")
	var stdout, stderr bytes.Buffer

	err = BuildArchive(
		t.Context(),
		imagesource.BuildConfig{
			Context:    contextPath,
			Dockerfile: dockerfilePath,
		},
		ocispec.Platform{
			OS:           "linux",
			Architecture: "arm64",
			Variant:      "v8",
		},
		outputPath,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"build\n",
		"--layers\n",
		"--format\noci\n",
		"--file\n" + dockerfilePath + "\n",
		"--platform\nlinux/arm64/v8\n",
		"--tag\noci-archive:" + outputPath + "\n",
		contextPath + "\n",
	} {
		if !strings.Contains(string(arguments), value) {
			t.Fatalf(
				"buildah arguments %q do not contain %q",
				arguments,
				value,
			)
		}
	}
	if stdout.String() != "build output\n" ||
		stderr.String() != "build error\n" {
		t.Fatalf(
			"output = stdout %q, stderr %q",
			stdout.String(),
			stderr.String(),
		)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("OCI output: %v", err)
	}
}

func TestBuildArchiveRequiresBuildah(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := BuildArchive(
		t.Context(),
		imagesource.BuildConfig{
			Context:    "/tmp",
			Dockerfile: "/tmp/Dockerfile",
		},
		ocispec.Platform{OS: "linux", Architecture: "amd64"},
		filepath.Join(t.TempDir(), "image.tar"),
		nil,
		nil,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "buildah is required") {
		t.Fatalf("error = %v", err)
	}
}
