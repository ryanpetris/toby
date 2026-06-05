package copilot

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	copilotconfig "petris.dev/toby/internal/dirty/tools/copilot/config"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/tooltest"
)

type fakeNPM struct{ tools.Base }

func TestSandboxContextSetupAddsCustomInstructionsDir(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"))
	sandbox.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] = "/existing"

	if err := cp.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPrefix := copilotconfig.InstructionsDir(layout.Context) + ","
	got := sandbox.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"]
	if !strings.HasPrefix(got, wantPrefix) || !strings.Contains(got, "/existing") {
		t.Fatalf("COPILOT_CUSTOM_INSTRUCTIONS_DIRS = %q", got)
	}
}

func TestRegisterContextFilesWritesCopilotFiles(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, service := newTestCopilot(t, filepath.Join(home, "context"))
	registrar := cp.(tools.ContextFileRegistrar)
	if _, err := service.AddInstruction(context.Background(), "user-instructions.md", []byte("# user instructions\n"), 0); err != nil {
		t.Fatal(err)
	}

	if err := registrar.RegisterContextFiles(context.Background(), tools.ContextOptions{}); err != nil {
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
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.Launch(context.Background(), []string{"--allow-all-tools"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(layout.Context), "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestLaunchYoloAppendsAllowAllTools(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	yes := true
	if err := cp.PrepareHost(context.Background(), &tools.Options{Yolo: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot", "--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(layout.Context), "--allow-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}

	got = nil
	plain, plainSandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime2", "toby", "context"))
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
	if slices.Contains(got, "--allow-all-tools") {
		t.Fatalf("argv = %#v, unexpected --allow-all-tools", got)
	}
}

func newTestCopilot(t *testing.T, contextDir string) (tools.Tool, *tooltest.Sandbox, *contextfiles.Service) {
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
