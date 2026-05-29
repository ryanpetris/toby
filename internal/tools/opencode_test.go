package tools

import (
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
)

func TestOpenCodeSetsSyntheticConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, StateHome: filepath.Join(home, ".state"), SandboxRoot: filepath.Join(home, "sandboxes")}
	oc := newOpenCodeTool(paths)
	run := &tool.RunContext{Env: tool.Environment{}}
	if err := oc.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".state", "toby", "static", "opencode")
	if run.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", run.Env["OPENCODE_CONFIG_DIR"], want)
	}
}
