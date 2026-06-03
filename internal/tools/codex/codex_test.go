package codex

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
)

type fakeNPM struct{ tool.Base }

func TestLaunchAddsTobyConfigOverrides(t *testing.T) {
	home := t.TempDir()
	cdx, sandbox, service := newTestCodex(t, filepath.Join(home, "context"))
	if _, err := service.AddInstruction(context.Background(), "user-instructions.md", []byte("# user instructions\n"), 0); err != nil {
		t.Fatal(err)
	}
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
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
	cdx, sandbox, _ := newTestCodex(t, filepath.Join(home, "context"))
	called := false
	sandbox.ExecFunc = func(_ context.Context, _ []string, _ tool.ExecOptions) (int, error) {
		called = true
		return 0, nil
	}

	if err := cdx.SandboxInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatalf("SandboxInit should not write or link Codex profile files")
	}
}

func TestLaunchYoloBypassesApprovals(t *testing.T) {
	home := t.TempDir()
	cdx, sandbox, _ := newTestCodex(t, filepath.Join(home, "context"))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	yes := true
	if err := cdx.HostInit(context.Background(), &tool.CommandOptions{Yolo: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := cdx.Launch(context.Background(), []string{"--model", "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, missing --dangerously-bypass-approvals-and-sandbox", got)
	}

	got = nil
	plain, plainSandbox, _ := newTestCodex(t, filepath.Join(home, "context2"))
	plainSandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	if err := plain.HostInit(context.Background(), &tool.CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plain.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("argv = %#v, unexpected --dangerously-bypass-approvals-and-sandbox", got)
	}
}

func newTestCodex(t *testing.T, contextDir string) (tool.Tool, *tooltest.Sandbox, *contextfiles.Service) {
	t.Helper()
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox, ContextFiles: contextFiles}).Service, sandbox, contextFiles
}
