package claude

// Tests Claude's tool lifecycle and runtime-specific launch arguments.

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	claudeconfig "petris.dev/toby/internal/tools/builtin/claude/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/fake"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestClaudeSetsConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home}
	sandbox := fake.NewSandbox()
	var claude tools.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(testConfig(t, false)),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Provide(sessionconfig.NewHolder),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"tools"`
		}) {
			for _, item := range params.Tools {
				if item.Name() == Name {
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
	claude, sandbox := newTestClaude(t, testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := claude.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := claude.Launch(context.Background(), []string{"--model", "opus"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("argv = %#v, missing --dangerously-skip-permissions", got)
	}

	got = nil
	plain, plainSandbox := newTestClaude(t, testConfig(t, false))
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

func newTestClaude(t *testing.T, cfg *appconfig.LaunchHolder) (tools.Tool, *fake.Sandbox) {
	t.Helper()
	sandbox := fake.NewSandbox()
	return provide(params{Sandbox: sandbox, SessionConfig: sessionconfig.NewHolder(), Config: cfg}).Service, sandbox
}

// testConfig builds an appconfig.Service for tests, optionally with yolo folded in
// the way a launch would.
func testConfig(t *testing.T, yolo bool) *appconfig.LaunchHolder {
	t.Helper()
	base, err := appconfig.Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !yolo {
		return appconfig.NewLaunchHolder(base)
	}
	enabled := true
	return appconfig.NewLaunchHolder(
		base.WithOverrides(appconfig.LaunchOverrides{Yolo: &enabled}),
	)
}

func TestNativeLaunchUsesNativeMCPAndSettingsOnly(t *testing.T) {
	recorder := fake.NewSandbox()
	native := &nativeClaudeSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	tool := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
		Config:        testConfig(t, false),
	}).Service

	var got []string
	recorder.ExecFunc = func(
		_ context.Context,
		argv []string,
		_ sandboxapi.ExecOptions,
	) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := tool.Launch(t.Context(), []string{"--model", "opus"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"claude",
		"--mcp-config", claudeconfig.NativeMCPPath,
		"--settings", claudeconfig.NativeSettingsPath,
		"--model", "opus",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native argv = %#v, want %#v", got, want)
	}
	for _, arg := range got {
		if arg == "--append-system-prompt-file" {
			t.Fatalf("native argv contains an instruction-file override: %#v", got)
		}
	}
}

type nativeClaudeSandbox struct {
	*fake.Sandbox
}

func (*nativeClaudeSandbox) UsesNativeToolFiles() bool {
	return true
}
