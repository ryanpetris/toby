package codex

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	appconfig "petris.dev/toby/config/app"
	"petris.dev/toby/config/session"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/fake"
)

type fakeNPM struct{ tools.Base }

func TestLaunchAddsTobyConfigOverrides(t *testing.T) {
	home := t.TempDir()
	cdx, sandbox, holder := newTestCodex(t, filepath.Join(home, "context"), testConfig(t, false))
	holder.Set(sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cdx.Launch(context.Background(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"codex",
		"-c", `mcp_servers.toby.url='http://127.0.0.1:12345/proxy/toby'`,
		"-c", `mcp_servers.toby.enabled=true`,
		"-c", `developer_instructions="# user instructions\n"`,
		"--model", "gpt-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestSandboxInitDoesNotLinkProfile(t *testing.T) {
	home := t.TempDir()
	cdx, sandbox, _ := newTestCodex(t, filepath.Join(home, "context"), testConfig(t, false))
	called := false
	sandbox.ExecFunc = func(_ context.Context, _ []string, _ sandboxapi.ExecOptions) (int, error) {
		called = true
		return 0, nil
	}

	if err := cdx.InitSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatalf("SandboxInit should not write or link Codex profile files")
	}
}

func TestLaunchYoloBypassesApprovals(t *testing.T) {
	home := t.TempDir()
	cdx, sandbox, _ := newTestCodex(t, filepath.Join(home, "context"), testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cdx.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := cdx.Launch(context.Background(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, missing --dangerously-bypass-approvals-and-sandbox", got)
	}

	got = nil
	plain, plainSandbox, _ := newTestCodex(t, filepath.Join(home, "context2"), testConfig(t, false))
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
	if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, unexpected --dangerously-bypass-approvals-and-sandbox", got)
	}
}

func newTestCodex(t *testing.T, contextDir string, cfg *appconfig.Service) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox(contextDir)
	holder := sessionconfig.NewHolder()
	tool := Provide(Params{Sandbox: sandbox, SessionConfig: holder, Config: cfg}).Service
	return tool, sandbox, holder
}

// testConfig builds an appconfig.Service for tests, optionally with yolo folded in.
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
