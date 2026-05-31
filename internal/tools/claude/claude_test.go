package claude

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/npm"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestClaudeSetsConfigDir(t *testing.T) {
	home := t.TempDir()
	sandboxHome := filepath.Join(home, "sandbox-home")
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	sandbox.PathsValue.Home = sandboxHome
	var claude tool.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(tool.SandboxService)))),
		fx.Provide(contextfiles.NewService),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			Claude tool.Tool `name:"claude"`
		}) {
			claude = params.Claude
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if err := claude.SandboxContextSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(sandboxHome, ".config", "claude")
	if sandbox.Env["CLAUDE_CONFIG_DIR"] != want {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", sandbox.Env["CLAUDE_CONFIG_DIR"], want)
	}
}

func TestContextFlags(t *testing.T) {
	contextDir := "/run/user/1000/toby/context"
	base := filepath.Join(contextDir, "claude")
	flags := contextFlags(contextDir)

	for _, want := range [][2]string{
		{"--mcp-config", filepath.Join(base, "mcp.json")},
		{"--settings", filepath.Join(base, "settings.json")},
		{"--append-system-prompt-file", filepath.Join(base, "instructions.md")},
	} {
		i := slices.Index(flags, want[0])
		if i == -1 || i+1 >= len(flags) || flags[i+1] != want[1] {
			t.Fatalf("flag %q missing or wrong value in %v", want[0], flags)
		}
	}
	if slices.Contains(flags, "--plugin-dir") {
		t.Fatalf("unexpected --plugin-dir: %v", flags)
	}
}
