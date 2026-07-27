package lifecycle

// Verifies dependency-ordered transient asset collection and cross-tool
// collision rejection.

import (
	"testing"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
)

type runtimeAssetTool struct {
	tools.Base
	assets []runtimeassets.Asset
	err    error
}

var _ tools.Tool = runtimeAssetTool{}
var _ runtimeassets.Contributor = runtimeAssetTool{}

func (t runtimeAssetTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	return t.assets, t.err
}

func TestRuntimeAssetsCollectsSelectedContributors(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		runtimeAssetTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "npm"}},
			assets: []runtimeassets.Asset{{
				Target: layout.Runtime + "/npm/install.sh",
				Data:   []byte("npm"),
				Mode:   0o500,
			}},
		},
		runtimeAssetTool{
			Base: tools.Base{Metadata: tools.Metadata{
				Name:         "agent",
				Dependencies: []string{"npm"},
			}},
			assets: []runtimeassets.Asset{{
				Target: layout.Runtime + "/agent/wrapper",
				Data:   []byte("agent"),
				Mode:   0o500,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"npm", "agent"}, "agent")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RuntimeAssets(set); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAssetsRejectsCrossToolCollision(t *testing.T) {
	target := layout.Runtime + "/shared/install.sh"
	registry, err := tools.NewRegistry([]tools.Tool{
		runtimeAssetTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "first"}},
			assets: []runtimeassets.Asset{{
				Target: target,
				Data:   []byte("first"),
				Mode:   0o500,
			}},
		},
		runtimeAssetTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "second"}},
			assets: []runtimeassets.Asset{{
				Target: target,
				Data:   []byte("second"),
				Mode:   0o500,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"first", "second"}, "second")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RuntimeAssets(set); err == nil {
		t.Fatal("cross-tool runtime asset collision was accepted")
	}
}
