package grok

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	grokconfig "petris.dev/toby/tools/builtin/grok/config"
	"petris.dev/toby/tools/fake"
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
	if len(sandbox.Mounts) != 1 || sandbox.Mounts[0].Key != (mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "state"}) || sandbox.Mounts[0].Target != filepath.Join(layout.Home, ".grok") {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
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
		if file.Path != grokconfig.StaticConfigPath {
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

func TestSandboxInitLinksManagedConfig(t *testing.T) {
	home := t.TempDir()
	contextDir := filepath.Join(home, "context")
	gr, sandbox, _ := newTestGrok(t, contextDir)

	if err := gr.InitSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(layout.Home, ".grok")
	if !reflect.DeepEqual(sandbox.Dirs, []string{wantDir}) {
		t.Fatalf("dirs = %#v, want %#v", sandbox.Dirs, []string{wantDir})
	}
	wantLink := filepath.Join(layout.Home, ".grok", "managed_config.toml")
	if sandbox.Symlinks[wantLink] != grokconfig.ConfigPath(layout.Context) {
		t.Fatalf("symlinks = %#v", sandbox.Symlinks)
	}
}

func TestLaunchAddsRules(t *testing.T) {
	home := t.TempDir()
	gr, sandbox, holder := newTestGrok(t, filepath.Join(home, "context"))
	holder.Set(sessionconfig.Config{
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := gr.Launch(context.Background(), []string{"--model", "grok-code-fast-1"}); err != nil {
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
