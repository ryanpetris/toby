package cursor

// Tests Cursor's lifecycle and runtime-specific launch arguments.

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	sessionconfig "petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"
)

func TestCursorHostInitRegistersManagedMount(t *testing.T) {
	cur, sandbox, _ := newTestCursor(t, testConfig(t, false))
	if err := cur.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("binds = %#v", sandbox.Binds)
	}
	want := []mount.Request{
		{
			Key:    mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "state"},
			Target: filepath.Join(layout.Home, ".cursor"),
			Access: mount.AccessRegular,
		},
		{
			Key:    mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "config"},
			Target: filepath.Join(layout.Home, ".config", "cursor"),
			Access: mount.AccessRegular,
		},
		{
			Key:    mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "data"},
			Target: filepath.Join(layout.Home, ".local", "share", "cursor-agent"),
			Access: mount.AccessRegular,
		},
	}
	if !reflect.DeepEqual(sandbox.Mounts, want) {
		t.Fatalf("mounts = %#v, want %#v", sandbox.Mounts, want)
	}
}

func TestConfigureSandboxSetsConfigDirAndPath(t *testing.T) {
	cur, sandbox, _ := newTestCursor(t, testConfig(t, false))
	if err := cur.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := sandbox.Env["CURSOR_CONFIG_DIR"], filepath.Join(layout.Home, ".cursor"); got != want {
		t.Fatalf("CURSOR_CONFIG_DIR = %q, want %q", got, want)
	}
	if got, want := sandbox.Env["AGENT_CLI_CREDENTIAL_STORE"], "file"; got != want {
		t.Fatalf("AGENT_CLI_CREDENTIAL_STORE = %q, want %q", got, want)
	}
	if got, want := sandbox.Env["PATH"], filepath.Join(layout.Home, ".local", "bin"); got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestLaunchApprovesMCPAndDisablesNestedSandbox(t *testing.T) {
	cur, sandbox, _ := newTestCursor(t, testConfig(t, false))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cur.Launch(context.Background(), []string{"--model", "auto"}); err != nil {
		t.Fatal(err)
	}
	want := []string{cursorCommand, "--approve-mcps", "--sandbox", "disabled", "--model", "auto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchYoloAppendsForce(t *testing.T) {
	cur, sandbox, _ := newTestCursor(t, testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cur.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := cur.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--force") {
		t.Fatalf("argv = %#v, missing --force", got)
	}

	got = nil
	plain, plainSandbox, _ := newTestCursor(t, testConfig(t, false))
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
	if slices.Contains(got, "--force") {
		t.Fatalf("argv = %#v, unexpected --force", got)
	}
}

func TestInstallSkipsWhenBinaryExists(t *testing.T) {
	cur, sandbox, _ := newTestCursor(t, testConfig(t, false))
	var calls [][]string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		return 0, nil
	}

	if err := cur.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"which", cursorCommand}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestUpgradeRunsInstaller(t *testing.T) {
	arch, err := assetArch()
	if err != nil {
		t.Skip(err)
	}
	cur, sandbox, _ := newTestCursor(t, testConfig(t, false))
	var calls [][]string
	var options []sandboxapi.ExecOptions
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		opts sandboxapi.ExecOptions,
	) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		options = append(options, opts)
		return 0, nil
	}

	if err := cur.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{filepath.Join(layout.Runtime, filepath.FromSlash(cursorInstallPath)), arch}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if len(options) != 1 || options[0].Status != "Installing" {
		t.Fatalf("install options = %#v", options)
	}
}

func newTestCursor(t *testing.T, cfg *appconfig.LaunchHolder) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox()
	holder := sessionconfig.NewHolder()
	tool := provide(params{Sandbox: sandbox, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

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
