package tools

import (
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestOpenCodeSetsSyntheticConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, StateHome: filepath.Join(home, ".state"), SandboxRoot: filepath.Join(home, "sandboxes")}
	var oc tool.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Provide(newOpenCodeTool),
		fx.Populate(&oc),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	run := &tool.RunContext{Env: tool.Environment{}}
	if err := oc.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".state", "toby", "static", "opencode")
	if run.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", run.Env["OPENCODE_CONFIG_DIR"], want)
	}
}
