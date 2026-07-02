package dcode

// Tests for the Deep Agents Code tool lifecycle and launch argv construction.

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/exitcode"
	appconfig "petris.dev/toby/internal/config/app"
	dcodeconfig "petris.dev/toby/internal/tools/builtin/dcode/config"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"
)

func TestDcodeDeclaresUVDependency(t *testing.T) {
	svc := Provide(Params{Sandbox: fake.NewSandbox("/toby/context"), ContextFiles: contextfiles.NewService(), SessionConfig: sessionconfig.NewHolder(), Config: testConfig(t, false)}).Service
	if got := svc.Dependencies(); len(got) != 1 || got[0] != uv.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}

func TestRegisterContextFilesWritesDcodeFiles(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	registrar := dc.(tools.ContextFileRegistrar)

	if err := registrar.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	if !hasFile(sandbox.Files, dcodeconfig.MCPConfigPath) {
		t.Fatalf("dcode mcp config not registered: %#v", sandbox.Files)
	}
}

func TestInstallSkipsWhenDcodeExists(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
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

func TestInstallForceRunsUVToolInstall(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := &deepAgentsTool{Simple: newTestSimple(sandbox)}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := svc.Install(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want := []string{"uv", "tool", "install", "deepagents-code", "--force"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestInstallFailsWhenUVToolInstallFails(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
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
	want := [][]string{{"which", "dcode"}, {"uv", "tool", "install", "deepagents-code"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLaunchDefaultsToTobyAgentAndWritesInstructions(t *testing.T) {
	dc, sandbox, _ := newTestDcode(t, testConfig(t, false))

	got, err := dc.LaunchCommand(context.Background(), []string{"--model", "openai:gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dcode", "--mcp-config", dcodeconfig.MCPConfigPath, "--agent", "toby", "--model", "openai:gpt-5.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	agentsPath := filepath.Join(layout.Home, ".deepagents", "toby", "AGENTS.md")
	if !hasFile(sandbox.Files, agentsPath) {
		t.Fatalf("files = %#v", sandbox.Files)
	}
	if len(sandbox.Symlinks) != 0 {
		t.Fatalf("symlinks = %#v", sandbox.Symlinks)
	}
}

func TestLaunchRespectsExplicitAgent(t *testing.T) {
	for _, extra := range [][]string{{"--agent", "custom"}, {"--agent=custom"}, {"-a", "custom"}} {
		dc, sandbox, _ := newTestDcode(t, testConfig(t, false))

		got, err := dc.LaunchCommand(context.Background(), extra)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(got, "toby") || slices.Contains(got, "--agent") && !slices.Contains(extra, "--agent") {
			t.Fatalf("argv = %#v, extra = %#v", got, extra)
		}
		if len(sandbox.Symlinks) != 0 {
			t.Fatalf("symlinks = %#v, extra = %#v", sandbox.Symlinks, extra)
		}
		if hasFile(sandbox.Files, filepath.Join(layout.Home, ".deepagents", "toby", "AGENTS.md")) {
			t.Fatalf("files = %#v, extra = %#v", sandbox.Files, extra)
		}
	}
}

func TestLaunchYoloAppendsAutoApprove(t *testing.T) {
	dc, _, _ := newTestDcode(t, testConfig(t, true))

	if err := dc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := dc.LaunchCommand(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "-y") {
		t.Fatalf("argv = %#v, missing -y", got)
	}
}

func TestLaunchConfiguresExplicitOpenAIModelProvider(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Providers: []sessionconfig.Provider{{Type: providerTypeOpenAI, URL: "http://127.0.0.1:12345/proxy/openai"}}})

	if _, err := dc.LaunchCommand(context.Background(), []string{"--model", "openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if sandbox.Env["DEEPAGENTS_CODE_OPENAI_API_KEY"] != "toby" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
	if sandbox.Env["DEEPAGENTS_CODE_OPENAI_BASE_URL"] != "http://127.0.0.1:12345/proxy/openai" {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func TestLaunchDoesNotGuessProviderWhenAmbiguous(t *testing.T) {
	dc, sandbox, holder := newTestDcode(t, testConfig(t, false))
	holder.Set(sessionconfig.Config{Providers: []sessionconfig.Provider{
		{Type: providerTypeOpenAI, URL: "http://127.0.0.1:12345/proxy/one"},
		{Type: providerTypeOpenAI, URL: "http://127.0.0.1:12345/proxy/two"},
	}})

	if _, err := dc.LaunchCommand(context.Background(), []string{"--model=openai:gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sandbox.Env["DEEPAGENTS_CODE_OPENAI_BASE_URL"]; ok {
		t.Fatalf("env = %#v", sandbox.Env)
	}
}

func newTestDcode(t *testing.T, cfg *appconfig.Service) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox("/toby/context")
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	holder := sessionconfig.NewHolder()
	tool := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

func newTestSimple(sandbox *fake.Sandbox) *kit.Simple {
	return kit.NewSimple(sandbox, tools.Base{Metadata: Meta}, nil, nil)
}

func testConfig(t *testing.T, yolo bool) *appconfig.Service {
	t.Helper()
	base, err := appconfig.Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !yolo {
		return base
	}
	enabled := true
	return base.WithOverrides(appconfig.LaunchOverrides{Yolo: &enabled})
}

func hasFile(files []contextfiles.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}
