package copilot

// Tests Copilot's lifecycle and runtime-specific launch arguments.

import (
	"context"
	"reflect"
	"slices"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	copilotconfig "petris.dev/toby/internal/tools/builtin/copilot/config"
	"petris.dev/toby/internal/tools/fake"
)

func TestInstallAllowsCopilotPackageScripts(t *testing.T) {
	cp, sandbox, _ := newTestCopilot(t, testConfig(t, false))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		if len(argv) != 0 && argv[0] == "which" {
			return 1, nil
		}
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.Install(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"npm", "install", "-g", "--allow-scripts=@github/copilot", "@github/copilot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchAddsAdditionalMCPConfig(t *testing.T) {
	cp, sandbox, _ := newTestCopilot(t, testConfig(t, false))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.Launch(context.Background(), []string{"--allow-all-tools"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.NativeMCPPath, "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchYoloAppendsAllowAllTools(t *testing.T) {
	cp, sandbox, _ := newTestCopilot(t, testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.NativeMCPPath, "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}

	got = nil
	plain, plainSandbox, _ := newTestCopilot(t, testConfig(t, false))
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
	if slices.Contains(got, "--allow-all-tools") {
		t.Fatalf("argv = %#v, unexpected --allow-all-tools", got)
	}
}

func TestNativeSandboxUsesNativeFilesAndHighestPriorityMCPFlag(t *testing.T) {
	recorder := fake.NewSandbox()
	native := &nativeCopilotSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	tool := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
		Config:        testConfig(t, false),
	}).Service

	if err := tool.ConfigureSandbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, found := recorder.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"]; found {
		t.Fatalf("native environment redirects Copilot instructions: %#v", recorder.Env)
	}

	var got []string
	recorder.ExecFunc = func(
		_ context.Context,
		argv []string,
		_ sandboxapi.ExecOptions,
	) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	if err := tool.Launch(t.Context(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"copilot",
		"--additional-mcp-config",
		"@" + copilotconfig.NativeMCPPath,
		"--model",
		"gpt-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native argv = %#v", got)
	}
}

func newTestCopilot(t *testing.T, cfg *appconfig.LaunchHolder) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox()
	holder := sessionconfig.NewHolder()
	tool := provide(params{Sandbox: sandbox, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

// testConfig builds an appconfig.Service for tests, optionally with yolo folded in.
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

type nativeCopilotSandbox struct {
	*fake.Sandbox
}

func (*nativeCopilotSandbox) UsesNativeToolFiles() bool {
	return true
}
