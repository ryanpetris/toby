package storage

// Resolves an optional immutable rootfs seed directory before invoking the
// platform-specific no-follow copier.

import (
	"context"
	"fmt"
	"path"
	"strings"

	"petris.dev/toby/internal/storage/safefs"
)

func (s *Store) seedDirectory(
	ctx context.Context,
	destination *safefs.Directory,
	seed SeedSource,
) error {
	if seed.ImagePath == "" {
		return nil
	}
	if !path.IsAbs(seed.ImagePath) || path.Clean(seed.ImagePath) != seed.ImagePath {
		return fmt.Errorf("seed image path must be clean and absolute: %q", seed.ImagePath)
	}
	if seed.Root == nil {
		return fmt.Errorf("seed root directory is required")
	}

	description := seed.RootDescription
	if description == "" {
		description = seed.Root.Name()
	}
	if description == "" {
		description = "<rootfs>"
	}
	relative := strings.TrimPrefix(seed.ImagePath, "/")
	source, found, err := openImmutableSeedDirectory(
		seed.Root,
		description,
		relative,
		s.logger,
	)
	if err != nil {
		return fmt.Errorf("open immutable seed root: %w", err)
	}
	if !found {
		return nil
	}

	copyErr := copySeedDirectory(
		ctx,
		source,
		destination,
		s.uid,
		s.gid,
		s.limits,
		s.logger,
	)
	s.logger.DebugError("close immutable seed source", source.Close())
	return copyErr
}
