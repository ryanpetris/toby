package copilot

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	copilotconfig "petris.dev/toby/internal/tools/copilot/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"
)

type fakeNPM struct{ tool.Base }

func TestSandboxContextSetupAddsCustomInstructionsDir(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"))
	sandbox.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] = "/existing"

	if err := cp.SandboxContextSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPrefix := copilotconfig.InstructionsDir(sandbox.Paths().Context) + ","
	got := sandbox.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"]
	if !strings.HasPrefix(got, wantPrefix) || !strings.Contains(got, "/existing") {
		t.Fatalf("COPILOT_CUSTOM_INSTRUCTIONS_DIRS = %q", got)
	}
}

func TestRegisterContextFilesWritesCopilotFiles(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, service := newTestCopilot(t, filepath.Join(home, "context"))
	registrar := cp.(tool.ContextFileTool)
	if _, err := service.AddInstruction(context.Background(), "GIT_AGENTS.md", []byte("# git\n"), 0); err != nil {
		t.Fatal(err)
	}

	if err := registrar.RegisterContextFiles(context.Background(), tool.ContextOptions{}); err != nil {
		t.Fatal(err)
	}
	files := sandbox.Files
	if !hasFile(files, copilotconfig.StaticMCPPath) || !hasFile(files, copilotconfig.StaticInstructionsPath) {
		t.Fatalf("copilot context files not registered: %#v", files)
	}
}

func TestLaunchAddsAdditionalMCPConfig(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.Launch(context.Background(), []string{"--allow-all-tools"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(sandbox.Paths().Context), "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func newTestCopilot(t *testing.T, contextDir string) (tool.Tool, *tooltest.Sandbox, *contextfiles.Service) {
	t.Helper()
	home := t.TempDir()
	sandbox := tooltest.NewSandbox(contextDir)
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	return Provide(Params{Paths: config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}, Sandbox: sandbox, ContextFiles: contextFiles}).Service, sandbox, contextFiles
}

func hasFile(files []contextfiles.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}
