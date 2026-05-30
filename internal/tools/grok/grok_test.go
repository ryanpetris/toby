package grok

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/grokconfig"
	"petris.dev/toby/internal/tool"
)

func TestGrokHostStateCreatesAndBindsStateRoot(t *testing.T) {
	home := t.TempDir()
	stateRoot := filepath.Join(home, "state-root")
	gr := Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}}).Service
	opts := &tool.CommandOptions{ToolStates: tool.ToolStateSettings{Tools: map[string]tool.ToolStateConfig{tool.GrokToolName: {State: tool.ToolStateHost, StateRoot: stateRoot}}}}
	if err := gr.HostInit(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(stateRoot, ".grok")); err != nil || !info.IsDir() {
		t.Fatalf("host state dir not created: info=%v err=%v", info, err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{gr}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{tool.GrokToolName}, "")
	if err != nil {
		t.Fatal(err)
	}
	toolset.SetToolStates(opts.ToolStates)
	binds := toolset.Binds()
	if len(binds) != 1 || binds[0].HostPath != filepath.Join(stateRoot, ".grok") || binds[0].Target != tool.HomeTarget(".grok") {
		t.Fatalf("binds = %#v", binds)
	}
}

func TestRegisterContextFilesWritesGrokConfig(t *testing.T) {
	home := t.TempDir()
	gr := Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}}).Service.(tool.ContextFileTool)
	service := contextfiles.NewService()
	run := &tool.RunContext{ContextFiles: service.NewSession(filepath.Join(home, "context")), TobyMCPURL: "http://127.0.0.1:12345/proxy/toby"}
	if err := run.ContextFiles.AddInstructionBytes("GIT_AGENTS.md", []byte("# git\n"), 0); err != nil {
		t.Fatal(err)
	}

	if err := gr.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	files := run.ContextFiles.Files()
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
	sandboxHome := filepath.Join(home, "sandbox-home")
	contextDir := filepath.Join(home, "context")
	gr := Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}}).Service
	service := contextfiles.NewService()
	var got [][]string
	run := &tool.RunContext{
		Sandbox:      grokFakeSandbox{home: sandboxHome, contextDir: contextDir},
		ContextFiles: service.NewSession(contextDir),
		Exec: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append(got, append([]string(nil), argv...))
			return 0, nil
		},
	}

	if err := gr.SandboxInit(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mkdir", "-p", filepath.Join(sandboxHome, ".grok")},
		{"ln", "-sfn", grokconfig.ConfigPath(contextDir), filepath.Join(sandboxHome, ".grok", "managed_config.toml")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchAddsRules(t *testing.T) {
	home := t.TempDir()
	gr := Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}}).Service
	service := contextfiles.NewService()
	contextSession := service.NewSession(filepath.Join(home, "context"))
	if err := contextSession.AddInstructionBytes("GIT_AGENTS.md", []byte("# git\n"), 0); err != nil {
		t.Fatal(err)
	}
	var got []string
	run := &tool.RunContext{
		ContextFiles: contextSession,
		Extra:        []string{"--model", "grok-code-fast-1"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}

	if err := gr.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := []string{"grok", "--rules", "# git\n", "--model", "grok-code-fast-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

type grokFakeSandbox struct {
	home       string
	contextDir string
}

func (s grokFakeSandbox) HomeDir() string               { return s.home }
func (s grokFakeSandbox) Projects() string              { return "" }
func (s grokFakeSandbox) TobyRuntimeDir() string        { return filepath.Dir(s.contextDir) }
func (s grokFakeSandbox) TobyContextDir() string        { return s.contextDir }
func (s grokFakeSandbox) TobyOpenCodeConfigDir() string { return "" }
