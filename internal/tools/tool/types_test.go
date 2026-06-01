package tool

import (
	"path/filepath"
	"testing"
)

func TestToolStateSettingsMergeStateForAndResolveRoots(t *testing.T) {
	settings := ToolStateSettings{
		Default: ToolStateConfig{State: ToolStatePrivate, StateRoot: "~/default"},
		Tools: map[string]ToolStateConfig{
			OpenCodeToolName: {State: ToolStateHost},
		},
	}
	settings.Merge(ToolStateSettings{Tools: map[string]ToolStateConfig{
		OpenCodeToolName: {StateRoot: "state/opencode"},
		ClaudeToolName:   {State: ToolStateHost},
	}})

	if settings.StateFor(NpmToolName) != ToolStatePrivate || settings.StateFor(OpenCodeToolName) != ToolStateHost || settings.StateFor(DockerToolName) != ToolStatePrivate || (ToolStateSettings{}).StateFor(DockerToolName) != ToolStateHost {
		t.Fatalf("states = %#v", settings)
	}
	if settings.StateRootFor(OpenCodeToolName) != "state/opencode" || settings.StateRootFor(ClaudeToolName) != "~/default" {
		t.Fatalf("state roots = %#v", settings)
	}

	home := filepath.Join(string(filepath.Separator), "home", "demo")
	base := filepath.Join(home, "project")
	resolved, err := settings.ResolveStateRoots(home, base)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Default.StateRoot != filepath.Join(home, "default") || resolved.Tools[OpenCodeToolName].StateRoot != filepath.Join(base, "state", "opencode") {
		t.Fatalf("resolved = %#v", resolved)
	}
	if settings.Default.StateRoot != "~/default" {
		t.Fatalf("ResolveStateRoots mutated source: %#v", settings)
	}
}
