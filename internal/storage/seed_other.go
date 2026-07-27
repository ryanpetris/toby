//go:build !linux

package storage

// Reports that secure rootfs-to-native storage seeding requires Linux.

import (
	"context"
	"fmt"
	"os"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

func copySeedDirectory(
	context.Context,
	*os.File,
	*safefs.Directory,
	int,
	int,
	Limits,
	*diagnostic.Logger,
) error {
	return fmt.Errorf("secure persistent storage seeding requires Linux")
}
