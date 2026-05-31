package codex

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"
)

type fakeNPM struct{ tool.Base }

func (fakeNPM) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.AbsoluteTarget("/npm/bin")}
}

func TestLaunchAddsTobyConfigOverrides(t *testing.T) {
	home := t.TempDir()
	cdx := Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service
	service := contextfiles.NewService()
	contextSession := service.NewSession(filepath.Join(home, "context"))
	if err := contextSession.AddInstructionBytes("GIT_AGENTS.md", []byte("# git\n"), 0); err != nil {
		t.Fatal(err)
	}
	var got []string
	run := &tool.RunContext{
		ContextFiles: contextSession,
		TobyMCPURL:   "http://127.0.0.1:12345/proxy/toby",
		Extra:        []string{"--model", "gpt-5"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}

	if err := cdx.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"codex",
		"-c", `mcp_servers.toby.url='http://127.0.0.1:12345/proxy/toby'`,
		"-c", `mcp_servers.toby.enabled=true`,
		"-c", `developer_instructions="# git\n"`,
		"--model", "gpt-5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestSandboxInitDoesNotLinkProfile(t *testing.T) {
	home := t.TempDir()
	cdx := Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service
	called := false
	run := &tool.RunContext{
		Exec: func(_ context.Context, _ []string, _ tool.ExecOptions) (int, error) {
			called = true
			return 0, nil
		},
	}

	if err := cdx.SandboxInit(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatalf("SandboxInit should not write or link Codex profile files")
	}
}
