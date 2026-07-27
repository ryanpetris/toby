package lifecycle

// Verifies native-file contribution collection, ownership, and cross-tool
// collision rejection.

import (
	"errors"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
)

type toolFileTool struct {
	tools.Base
	files []toolfiles.File
	err   error
}

var _ tools.Tool = toolFileTool{}
var _ toolfiles.Contributor = toolFileTool{}

func (t toolFileTool) ToolFiles(toolfiles.Ownership) ([]toolfiles.File, error) {
	return t.files, t.err
}

func TestToolFilesCollectsAndSortsSelectedContributors(t *testing.T) {
	ownership := toolfiles.Ownership{UID: 1000, GID: 1001}
	registry, err := tools.NewRegistry([]tools.Tool{
		toolFileTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "first"}},
			files: []toolfiles.File{{
				Owner:  "first",
				Target: layout.Home + "/.first/config",
				Data:   []byte("first"),
				Mode:   0o600,
				UID:    ownership.UID,
				GID:    ownership.GID,
			}},
		},
		toolFileTool{
			Base: tools.Base{Metadata: tools.Metadata{
				Name:         "second",
				Dependencies: []string{"first"},
			}},
			files: []toolfiles.File{{
				Owner:  "second",
				Target: layout.Home + "/.second/AGENTS.md",
				Data:   []byte("second"),
				Mode:   0o644,
				UID:    ownership.UID,
				GID:    ownership.GID,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"second"}, "second")
	if err != nil {
		t.Fatal(err)
	}

	files, err := ToolFiles(set, ownership)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 ||
		files[0].Target != layout.Home+"/.first/config" ||
		files[1].Target != layout.Home+"/.second/AGENTS.md" {
		t.Fatalf("files = %#v", files)
	}
}

func TestToolFilesRejectsInvalidContribution(t *testing.T) {
	ownership := toolfiles.Ownership{UID: 1000, GID: 1001}
	for _, test := range []struct {
		name  string
		files []toolfiles.File
		err   error
	}{
		{
			name: "wrong owner",
			files: []toolfiles.File{{
				Owner:  "other",
				Target: layout.Home + "/config",
				Mode:   0o600,
				UID:    ownership.UID,
				GID:    ownership.GID,
			}},
		},
		{
			name: "wrong identity",
			files: []toolfiles.File{{
				Owner:  "tool",
				Target: layout.Home + "/config",
				Mode:   0o600,
				UID:    2000,
				GID:    ownership.GID,
			}},
		},
		{
			name: "contributor error",
			err:  errors.New("render failed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := tools.NewRegistry([]tools.Tool{
				toolFileTool{
					Base:  tools.Base{Metadata: tools.Metadata{Name: "tool"}},
					files: test.files,
					err:   test.err,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			set, err := registry.Build([]string{"tool"}, "tool")
			if err != nil {
				t.Fatal(err)
			}

			if _, err := ToolFiles(set, ownership); err == nil {
				t.Fatal("invalid native-file contribution was accepted")
			}
		})
	}
}

func TestToolFilesRejectsCrossToolTargetCollision(t *testing.T) {
	ownership := toolfiles.Ownership{UID: 1000, GID: 1001}
	target := layout.Home + "/shared/config"
	registry, err := tools.NewRegistry([]tools.Tool{
		toolFileTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "first"}},
			files: []toolfiles.File{{
				Owner:  "first",
				Target: target,
				Mode:   0o600,
				UID:    ownership.UID,
				GID:    ownership.GID,
			}},
		},
		toolFileTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "second"}},
			files: []toolfiles.File{{
				Owner:  "second",
				Target: target,
				Mode:   0o600,
				UID:    ownership.UID,
				GID:    ownership.GID,
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

	if _, err := ToolFiles(set, ownership); err == nil {
		t.Fatal("cross-tool native-file collision was accepted")
	}
}
