package runtimeassets

// Validates, clones, and deterministically orders byte-asset registrations.

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// Registry is an immutable collection of validated runtime assets.
type Registry struct {
	assets []Asset
}

// NewRegistry validates the complete registration set before retaining a
// detached, target-sorted copy.
func NewRegistry(assets []Asset) (*Registry, error) {
	normalized := make([]Asset, len(assets))
	for index, asset := range assets {
		if err := validateAsset(asset); err != nil {
			return nil, fmt.Errorf("runtime asset %d: %w", index, err)
		}

		normalized[index] = asset
		normalized[index].Data = append([]byte(nil), asset.Data...)
	}

	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Target < normalized[j].Target
	})
	for index, asset := range normalized {
		for earlier := range index {
			previous := normalized[earlier]
			switch {
			case previous.Target == asset.Target:
				return nil, fmt.Errorf(
					"duplicate runtime asset target %q",
					asset.Target,
				)
			case mount.TargetsOverlap(previous.Target, asset.Target):
				return nil, fmt.Errorf(
					"overlapping runtime asset targets %q and %q",
					previous.Target,
					asset.Target,
				)
			}
		}
	}

	return &Registry{assets: normalized}, nil
}

func validateAsset(asset Asset) error {
	if err := mount.ValidateTarget(asset.Target); err != nil {
		return fmt.Errorf("invalid target %q: %w", asset.Target, err)
	}
	if !strings.HasPrefix(asset.Target, layout.Runtime+"/") {
		return fmt.Errorf(
			"target %q must be strictly beneath %s",
			asset.Target,
			layout.Runtime,
		)
	}
	if asset.Mode&^fs.ModePerm != 0 {
		return fmt.Errorf(
			"mode %v contains non-permission bits",
			asset.Mode,
		)
	}
	if asset.Mode.Perm()&0o400 == 0 {
		return fmt.Errorf("mode %04o must be owner-readable", asset.Mode.Perm())
	}
	if asset.Mode.Perm()&0o022 != 0 {
		return fmt.Errorf(
			"mode %04o must not be group- or other-writable",
			asset.Mode.Perm(),
		)
	}

	return nil
}
