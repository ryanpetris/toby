package oci

// Coordinates filesystem locks, temporary-object cleanup, and immutable object
// keys within the per-user OCI image store.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	digest "github.com/opencontainers/go-digest"

	"petris.dev/toby/internal/storage/safefs"
)

func (s *Store) lockContext(
	ctx context.Context,
	name string,
) (*safefs.Lock, error) {
	return s.lockContextMode(ctx, name, safefs.LockExclusive)
}

func (s *Store) lockContextMode(
	ctx context.Context,
	name string,
	mode safefs.LockMode,
) (*safefs.Lock, error) {
	for {
		lock, err := s.root.Lock(name, mode, true)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, safefs.ErrWouldBlock) {
			return nil, err
		}

		timer := time.NewTimer(s.lockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func objectLockName(object string) string {
	return filepath.Join(
		"locks",
		"objects",
		digest.SHA256.FromString(object).Encoded()+".lock",
	)
}

func removeTemporaryObject(path string) error {
	if path == "" {
		return nil
	}
	if err := os.RemoveAll(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove temporary OCI object %q: %w", path, err)
	}
	return nil
}

func immutableObjectKey(spec Spec) (string, error) {
	if err := spec.Manifest.Digest.Validate(); err != nil {
		return "", fmt.Errorf("invalid OCI manifest digest: %w", err)
	}
	if spec.Manifest.Digest.Algorithm() != digest.SHA256 {
		return "", fmt.Errorf(
			"unsupported OCI manifest digest algorithm %q",
			spec.Manifest.Digest.Algorithm(),
		)
	}

	platformData, err := json.Marshal(spec.Platform)
	if err != nil {
		return "", fmt.Errorf("encode OCI object platform: %w", err)
	}
	platformDigest := digest.SHA256.FromBytes(platformData).Encoded()

	return filepath.Join(
		platformDigest,
		"sha256",
		spec.Manifest.Digest.Encoded(),
	), nil
}

func (s *Store) objectPath(key string) string {
	return filepath.Join(s.root.Path(), "objects", key)
}
