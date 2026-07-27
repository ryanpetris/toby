package client

// Verifies that the process-wide graph constructs the native session runner
// together with the complete built-in tool registry.

import (
	"path/filepath"
	"testing"

	"go.uber.org/fx"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/tools"
)

func TestProcessGraphConstructsNativeSessionRunner(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{
		Home:          home,
		XDGConfigHome: filepath.Join(home, ".config"),
		XDGCacheHome:  filepath.Join(home, ".cache"),
		XDGDataHome:   filepath.Join(home, ".local", "share"),
		ProjectRoot:   filepath.Join(home, "Projects"),
	}

	var runner run.Runner
	var registry *tools.Registry
	app := fx.New(
		processModule(),
		fx.Replace(paths, args(nil)),
		fx.Populate(&runner, &registry),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(*run.NativeRunner); !ok {
		t.Fatalf("session runner type = %T, want *run.NativeRunner", runner)
	}

	for _, name := range []string{"codex", "opencode", "docker"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("static tool registry does not contain %q", name)
		}
	}
}
