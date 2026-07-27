package dcode

// Tests for the Deep Agents Code tool lifecycle and launch argv construction.

import (
	"context"
	"reflect"
	"slices"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic/exitcode"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	dcodeconfig "petris.dev/toby/internal/tools/builtin/dcode/config"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/internal/tools/kit"
)

func TestDcodeDeclaresUVDependency(t *testing.T) {
	svc := provide(params{Sandbox: fake.NewSandbox(), SessionConfig: sessionconfig.NewHolder(), Config: testConfig(t, false)}).Service
	if got := svc.Dependencies(); len(got) != 1 || got[0] != uv.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}

func TestInstallSkipsWhenDcodeExists(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := &deepAgentsTool{Simple: newTestSimple(sandbox)}
	var execCalls [][]string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		execCalls = append(execCalls, append([]string(nil), argv...))
		return 0, nil
	}

	if err := svc.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"which", "dcode"}}; !reflect.DeepEqual(execCalls, want) {
		t.Fatalf("exec calls = %#v, want %#v", execCalls, want)
	}
}

func TestInstallUpgradeRunsUVToolInstall(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := &deepAgentsTool{Simple: newTestSimple(sandbox)}
	var got []string
	var gotOptions sandboxapi.ExecOptions
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		options sandboxapi.ExecOptions,
	) (int, error) {
		got = append([]string(nil), argv...)
		gotOptions = options
		return 0, nil
	}

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := []string{"uv", "tool", "install", "--upgrade", "--prerelease", "allow", "deepagents-code"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if gotOptions.Status != "Installing" {
		t.Fatalf("install status = %q", gotOptions.Status)
	}
}

func TestInstallFailsWhenUVToolInstallFails(t *testing.T) {
	sandbox := fake.NewSandbox()
	svc := &deepAgentsTool{Simple: newTestSimple(sandbox)}
	var calls [][]string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		calls = append(calls, append([]string(nil), argv...))
		if reflect.DeepEqual(argv, []string{"which", "dcode"}) {
			return 1, nil
		}
		return 7, nil
	}

	err := svc.Install(context.Background(), false)
	if err == nil || exitcode.FromError(err) != 7 {
		t.Fatalf("err = %v, exit code = %d", err, exitcode.FromError(err))
	}
	want := [][]string{
		{"which", "dcode"},
		{"uv", "tool", "install", "--prerelease", "allow", "deepagents-code"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchDefaultsToTobyAgentAndNativeMCP(t *testing.T) {
	dc, sandbox, _ := newTestDcode(t, testConfig(t, false))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := dc.Launch(context.Background(), []string{"--model", "openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"dcode", "--mcp-config", dcodeconfig.NativeMCPPath, "--agent", "toby", "--model", "openai:gpt-5.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchRespectsExplicitAgent(t *testing.T) {
	for _, extra := range [][]string{{"--agent", "custom"}, {"--agent=custom"}, {"-a", "custom"}} {
		dc, sandbox, _ := newTestDcode(t, testConfig(t, false))
		var got []string
		sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		}

		if err := dc.Launch(context.Background(), extra); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(got, "toby") || slices.Contains(got, "--agent") && !slices.Contains(extra, "--agent") {
			t.Fatalf("argv = %#v, extra = %#v", got, extra)
		}
	}
}

func TestLaunchYoloAppendsAutoApprove(t *testing.T) {
	dc, sandbox, _ := newTestDcode(t, testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := dc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := dc.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "-y") {
		t.Fatalf("argv = %#v, missing -y", got)
	}
}

func TestLaunchConfiguresExplicitOpenAIModelProvider(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Models: []sessionconfig.ModelsEndpoint{{
		Type:       providerTypeOpenAI,
		URL:        "http://127.0.0.1:12345/provider/openai",
		Credential: "synthetic-openai",
	}}})

	if err := dc.(*deepAgentsTool).PrepareLaunch(context.Background(), []string{"--model", "openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if sandbox.Env["DEEPAGENTS_CODE_OPENAI_API_KEY"] != "synthetic-openai" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
	if sandbox.Env["DEEPAGENTS_CODE_OPENAI_BASE_URL"] != "http://127.0.0.1:12345/provider/openai" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func TestLaunchConfiguresExplicitAnthropicModelProvider(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Models: []sessionconfig.ModelsEndpoint{{
		Type:       providerTypeAnthropic,
		URL:        "http://127.0.0.1:12345/provider/anthropic",
		Credential: "synthetic-anthropic",
	}}})

	if err := dc.(*deepAgentsTool).PrepareLaunch(context.Background(), []string{"-Manthropic:claude-sonnet"}); err != nil {
		t.Fatal(err)
	}
	if sandbox.Env["DEEPAGENTS_CODE_ANTHROPIC_API_KEY"] != "synthetic-anthropic" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
	if sandbox.Env["DEEPAGENTS_CODE_ANTHROPIC_BASE_URL"] != "http://127.0.0.1:12345/provider/anthropic" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func TestLaunchUsesPlaceholderForProviderWithoutCredential(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Models: []sessionconfig.ModelsEndpoint{{
		Type: providerTypeOpenAI,
		URL:  "http://127.0.0.1:12345/provider/openai",
	}}})

	if err := dc.(*deepAgentsTool).PrepareLaunch(context.Background(), []string{"--model=openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if sandbox.Env["DEEPAGENTS_CODE_OPENAI_API_KEY"] != "toby" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func TestLaunchDoesNotGuessProviderWhenAmbiguous(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Models: []sessionconfig.ModelsEndpoint{
		{Type: providerTypeOpenAI, URL: "http://127.0.0.1:12345/provider/one"},
		{Type: providerTypeOpenAI, URL: "http://127.0.0.1:12345/provider/two"},
	}})

	if err := dc.(*deepAgentsTool).PrepareLaunch(context.Background(), []string{"--model=openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sandbox.Env["DEEPAGENTS_CODE_OPENAI_BASE_URL"]; ok {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func TestNativeLaunchPreparesProviderAndUsesNativeFiles(t *testing.T) {
	recorder := fake.NewSandbox()
	native := &nativeDCodeSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	holder.Set(sessionconfig.Config{
		Models: []sessionconfig.ModelsEndpoint{{
			Type:       providerTypeOpenAI,
			URL:        "http://127.0.0.1:12345/provider/openai",
			Credential: "native-openai",
		}},
	})
	service := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
		Config:        testConfig(t, false),
	}).Service
	tool := service.(*deepAgentsTool)
	args := []string{"--model", "openai:gpt-5.5"}

	if err := tool.PrepareLaunch(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if recorder.Env["DEEPAGENTS_CODE_OPENAI_BASE_URL"] !=
		"http://127.0.0.1:12345/provider/openai" {
		t.Fatalf("prepared environment = %#v", recorder.Env)
	}
	if recorder.Env["DEEPAGENTS_CODE_OPENAI_API_KEY"] != "native-openai" {
		t.Fatalf("prepared environment = %#v", recorder.Env)
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
	if err := tool.Launch(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dcode",
		"--mcp-config", dcodeconfig.NativeMCPPath,
		"--agent", "toby",
		"--model", "openai:gpt-5.5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native argv = %#v, want %#v", got, want)
	}
}

func newTestDcode(t *testing.T, cfg *appconfig.LaunchHolder) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox()
	holder := sessionconfig.NewHolder()
	tool := provide(params{Sandbox: sandbox, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

func newTestSimple(sandbox *fake.Sandbox) *kit.Simple {
	return kit.NewSimple(sandbox, tools.Base{Metadata: Meta}, []string{".deepagents"}, nil, nil)
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

type nativeDCodeSandbox struct {
	*fake.Sandbox
}

func (*nativeDCodeSandbox) UsesNativeToolFiles() bool {
	return true
}
