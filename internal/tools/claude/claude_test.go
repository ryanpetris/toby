package claude

import (
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/npm"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestClaudeSetsConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGRuntimeDir: filepath.Join(home, "runtime"), SandboxRoot: filepath.Join(home, "sandboxes")}
	run := &tool.RunContext{Options: &tool.CommandOptions{}, Sandbox: fakeSandbox{home: home, runtime: filepath.Join(home, "runtime"), projects: filepath.Join(home, "Projects")}, Env: tool.Environment{}}
	var claude tool.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
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
	if err := claude.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "claude")
	if run.Env["CLAUDE_CONFIG_DIR"] != want {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", run.Env["CLAUDE_CONFIG_DIR"], want)
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

type fakeSandbox struct {
	home     string
	runtime  string
	projects string
}

func (s fakeSandbox) HomeDir() string { return s.home }

func (s fakeSandbox) Projects() string { return s.projects }

func (s fakeSandbox) TobyRuntimeDir() string { return filepath.Join(s.runtime, "toby") }

func (s fakeSandbox) TobyContextDir() string { return filepath.Join(s.TobyRuntimeDir(), "context") }

func (s fakeSandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.TobyContextDir(), "opencode")
}
