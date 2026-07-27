package oci

// Materializes a Dockerfile source through a cache-resident intermediate
// archive while leaving publishable object staging in the data store.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/storage/safefs"
)

func (s *Store) materializeBuild(
	ctx context.Context,
	build imagesource.BuildConfig,
	platform ocispec.Platform,
	layoutPath string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	cacheRoot, err := safefs.OpenOrCreateRoot(
		s.cachePath,
		safefs.DirectoryOptions{
			OwnerUID: s.uid,
			OwnerGID: s.gid,
			Logger:   s.logger,
		},
	)
	if err != nil {
		return fmt.Errorf("open Toby cache root for OCI build: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close Toby cache root after OCI build",
			cacheRoot.Close(),
		)
	}()

	imageCache, err := cacheRoot.MkdirAll("images")
	if err != nil {
		return fmt.Errorf("open OCI build cache: %w", err)
	}
	imageCache.RepairPrivateOwnershipAndMode()
	defer func() {
		s.logger.DebugError(
			"close OCI image cache after build",
			imageCache.Close(),
		)
	}()

	archiveCache, err := imageCache.MkdirAll("builds")
	if err != nil {
		return fmt.Errorf("open OCI build archive cache: %w", err)
	}
	archiveCache.RepairPrivateOwnershipAndMode()
	defer func() {
		s.logger.DebugError(
			"close OCI build archive cache",
			archiveCache.Close(),
		)
	}()

	temporary, err := os.MkdirTemp(
		archiveCache.Path(),
		"archive-",
	)
	if err != nil {
		return fmt.Errorf("create intermediate OCI build archive directory: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"remove intermediate OCI build archive",
			removeTemporaryObject(temporary),
			"path",
			temporary,
		)
	}()

	archivePath := filepath.Join(temporary, "image.oci.tar")
	if err := BuildArchive(
		ctx,
		build,
		platform,
		archivePath,
		stdout,
		stderr,
	); err != nil {
		return err
	}
	return extractOCIArchive(ctx, archivePath, layoutPath)
}
