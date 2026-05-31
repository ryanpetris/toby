package copilot

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/tool"
	copilotconfig "petris.dev/toby/internal/tools/copilot/config"
)

type fakeNPM struct{ tool.Base }

func (fakeNPM) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.AbsoluteTarget("/npm/bin")}
}

func TestSandboxContextSetupAddsCustomInstructionsDir(t *testing.T) {
	home := t.TempDir()
	cp := Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service
	sandbox := fakeSandbox{home: home, runtime: filepath.Join(home, "runtime"), projects: filepath.Join(home, "Projects")}
	run := &tool.RunContext{Sandbox: sandbox, Env: tool.Environment{"COPILOT_CUSTOM_INSTRUCTIONS_DIRS": "/existing"}}

	if err := cp.SandboxContextSetup(run); err != nil {
		t.Fatal(err)
	}
	wantPrefix := copilotconfig.InstructionsDir(sandbox.TobyContextDir()) + ","
	if !strings.HasPrefix(run.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"], wantPrefix) || !strings.Contains(run.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"], "/existing") {
		t.Fatalf("COPILOT_CUSTOM_INSTRUCTIONS_DIRS = %q", run.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"])
	}
}

func TestRegisterContextFilesWritesCopilotFiles(t *testing.T) {
	home := t.TempDir()
	cp := Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service.(tool.ContextFileTool)
	service := contextfiles.NewService()
	run := &tool.RunContext{ContextFiles: service.NewSession(filepath.Join(home, "context")), TobyMCPURL: "http://127.0.0.1:12345/proxy/toby"}
	if err := run.ContextFiles.AddInstructionBytes("GIT_AGENTS.md", []byte("# git\n"), 0); err != nil {
		t.Fatal(err)
	}

	if err := cp.RegisterContextFiles(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	files := run.ContextFiles.Files()
	if !hasFile(files, copilotconfig.StaticMCPPath) || !hasFile(files, copilotconfig.StaticInstructionsPath) {
		t.Fatalf("copilot context files not registered: %#v", files)
	}
}

func TestLaunchAddsAdditionalMCPConfig(t *testing.T) {
	home := t.TempDir()
	cp := Provide(Params{
		Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")},
		NPM:   fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}},
	}).Service
	sandbox := fakeSandbox{home: home, runtime: filepath.Join(home, "runtime"), projects: filepath.Join(home, "Projects")}
	var got []string
	run := &tool.RunContext{
		Sandbox: sandbox,
		Extra:   []string{"--allow-all-tools"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}

	if err := cp.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(sandbox.TobyContextDir()), "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func hasFile(files []contextfiles.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

type fakeSandbox struct {
	home     string
	runtime  string
	projects string
}

func (s fakeSandbox) HomeDir() string { return s.home }

func (s fakeSandbox) Projects() string { return s.projects }

func (s fakeSandbox) TobyRuntimeDir() string { return filepath.Join(s.runtime, "toby") }

func (s fakeSandbox) TobyContextDir() string { return filepath.Join(s.TobyRuntimeDir(), "context") }

func (s fakeSandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.TobyContextDir(), "opencode")
}
