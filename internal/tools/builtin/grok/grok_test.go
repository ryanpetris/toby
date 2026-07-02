package grok

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	grokconfig "petris.dev/toby/internal/tools/builtin/grok/config"
	"petris.dev/toby/internal/tools/fake"
	"petris.dev/toby/tools"
)

func TestGrokHostInitRegistersManagedMount(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	gr := Provide(Params{Sandbox: sandbox}).Service
	if err := gr.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("binds = %#v", sandbox.Binds)
	}
}

func TestRegisterContextFilesWritesGrokConfig(t *testing.T) {
	home := t.TempDir()
	gr, sandbox, holder := newTestGrok(t, filepath.Join(home, "context"))
	holder.Set(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"}},
	})
	registrar := gr.(tools.ContextFileRegistrar)

	if err := registrar.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	for _, file := range files {
		if file.Path != layout.Expand(grokconfig.ConfigPath) {
			continue
		}
		config := string(file.Data)
		if !strings.Contains(config, `[mcp_servers.toby]`) || !strings.Contains(config, `url = 'http://127.0.0.1:12345/proxy/toby'`) {
			t.Fatalf("config = %s", config)
		}
		return
	}
	t.Fatalf("config file not registered: %#v", files)
}

func TestLaunchAddsRules(t *testing.T) {
	home := t.TempDir()
	gr, _, holder := newTestGrok(t, filepath.Join(home, "context"))
	holder.Set(sessionconfig.Config{
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})

	got, err := gr.LaunchCommand(context.Background(), []string{"--model", "grok-code-fast-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grok", "--rules", "# user instructions\n", "--model", "grok-code-fast-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func newTestGrok(t *testing.T, contextDir string) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox(contextDir)
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	holder := sessionconfig.NewHolder()
	tool := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles, SessionConfig: holder}).Service
	return tool, sandbox, holder
}
