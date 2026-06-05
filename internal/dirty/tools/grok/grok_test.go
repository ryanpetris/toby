package grok

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	grokconfig "petris.dev/toby/internal/dirty/tools/grok/config"
	"petris.dev/toby/internal/dirty/tools/tooltest"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

func TestGrokHostInitRegistersManagedMount(t *testing.T) {
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	gr := Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox}).Service
	if err := gr.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("binds = %#v", sandbox.Binds)
	}
	if len(sandbox.Mounts) != 1 || sandbox.Mounts[0].Key != (mount.Key{Type: mount.TypeTool, Name: tools.GrokToolName, Purpose: "state"}) || sandbox.Mounts[0].Target != filepath.Join(layout.Home, ".grok") {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
}

func TestRegisterContextFilesWritesGrokConfig(t *testing.T) {
	home := t.TempDir()
	gr, sandbox, service := newTestGrok(t, filepath.Join(home, "context"))
	registrar := gr.(tools.ContextFileRegistrar)
	if _, err := service.AddInstruction(context.Background(), "user-instructions.md", []byte("# user instructions\n"), 0); err != nil {
		t.Fatal(err)
	}

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
	gr, sandbox, service := newTestGrok(t, filepath.Join(home, "context"))
	if _, err := service.AddInstruction(context.Background(), "user-instructions.md", []byte("# user instructions\n"), 0); err != nil {
		t.Fatal(err)
	}
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

func newTestGrok(t *testing.T, contextDir string) (tools.Tool, *tooltest.Sandbox, *contextfiles.Service) {
	t.Helper()
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox, ContextFiles: contextFiles}).Service, sandbox, contextFiles
}
