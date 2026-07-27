package lifecycle

// Collects transient installer and wrapper assets from the active concrete
// tools before the Bubblewrap run plan is built.

import (
	"fmt"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/tools"
)

// RuntimeAssets returns one validated immutable registry containing every
// contribution in tool dependency order. Registry validation rejects target
// collisions across tools before any runtime filesystem mutation.
func RuntimeAssets(set *tools.Toolset) (*runtimeassets.Registry, error) {
	var assets []runtimeassets.Asset
	if set != nil {
		for _, tool := range set.OrderedTools() {
			contributor, ok := tool.(runtimeassets.Contributor)
			if !ok {
				continue
			}

			contributed, err := contributor.RuntimeAssets()
			if err != nil {
				return nil, fmt.Errorf(
					"collect runtime assets from %s: %w",
					tool.Name(),
					err,
				)
			}
			assets = append(assets, contributed...)
		}
	}

	registry, err := runtimeassets.NewRegistry(assets)
	if err != nil {
		return nil, fmt.Errorf("validate tool runtime assets: %w", err)
	}
	return registry, nil
}
