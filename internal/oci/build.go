package oci

// Runs Buildah and exports one OCI image-layout tar.

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/imagesource"
)

// BuildArchive runs one Dockerfile build and writes its OCI archive to
// destination.
func BuildArchive(
	ctx context.Context,
	build imagesource.BuildConfig,
	platform ocispec.Platform,
	destination string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if ctx == nil {
		return fmt.Errorf("OCI build context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	buildah, err := exec.LookPath("buildah")
	if err != nil {
		return fmt.Errorf(
			"buildah is required to build OCI images: %w",
			err,
		)
	}

	contextPath, err := filepath.Abs(build.Context)
	if err != nil {
		return fmt.Errorf("resolve OCI build context: %w", err)
	}
	dockerfilePath, err := filepath.Abs(build.Dockerfile)
	if err != nil {
		return fmt.Errorf("resolve OCI build Dockerfile: %w", err)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve OCI build output: %w", err)
	}

	platformValue := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		platformValue += "/" + platform.Variant
	}
	command := exec.CommandContext(
		ctx,
		buildah,
		"build",
		"--layers",
		"--format",
		"oci",
		"--file",
		filepath.Clean(dockerfilePath),
		"--platform",
		platformValue,
		"--tag",
		"oci-archive:"+filepath.Clean(destination),
		filepath.Clean(contextPath),
	)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build OCI image with buildah: %w", err)
	}
	return nil
}
