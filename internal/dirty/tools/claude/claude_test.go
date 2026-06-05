package claude

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/dirty/tools/npm"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/tooltest"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestClaudeSetsConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	var claude tools.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Provide(contextfiles.NewService),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"toby.tools"`
		}) {
			for _, item := range params.Tools {
				if item.Name() == tools.ClaudeToolName {
					claude = item
				}
			}
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if err := claude.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(layout.Home, ".config", "claude")
	if sandbox.Env["CLAUDE_CONFIG_DIR"] != want {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", sandbox.Env["CLAUDE_CONFIG_DIR"], want)
	}
}

func TestLaunchYoloAppendsSkipPermissions(t *testing.T) {
	home := t.TempDir()
	claude, sandbox := newTestClaude(t, filepath.Join(home, "runtime", "toby", "context"))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	yes := true
	if err := claude.PrepareHost(context.Background(), &tools.Options{Yolo: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := claude.Launch(context.Background(), []string{"--model", "opus"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("argv = %#v, missing --dangerously-skip-permissions", got)
	}

	got = nil
	plain, plainSandbox := newTestClaude(t, filepath.Join(home, "runtime2", "toby", "context"))
	plainSandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	if err := plain.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := plain.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("argv = %#v, unexpected --dangerously-skip-permissions", got)
	}
}

func newTestClaude(t *testing.T, contextDir string) (tools.Tool, *tooltest.Sandbox) {
	t.Helper()
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox, ContextFiles: contextFiles}).Service, sandbox
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
