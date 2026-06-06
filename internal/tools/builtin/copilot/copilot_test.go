package copilot

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	contextfiles "petris.dev/toby/context/files"
	appconfig "petris.dev/toby/internal/config/app"
	copilotconfig "petris.dev/toby/internal/tools/builtin/copilot/config"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

type fakeNPM struct{ tools.Base }

func TestSandboxContextSetupAddsCustomInstructionsDir(t *testing.T) {
	home := t.TempDir()
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"), testConfig(t, false))
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
	cp, sandbox, holder := newTestCopilot(t, filepath.Join(home, "context"), testConfig(t, false))
	holder.Set(sessionconfig.Config{
		MCPServers:   []sessionconfig.MCPServer{{Name: "toby", URL: "http://127.0.0.1:12345/proxy/toby"}},
		Instructions: sessionconfig.Instructions{Contents: [][]byte{[]byte("# user instructions\n")}},
	})
	registrar := cp.(tools.ContextFileRegistrar)

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
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"), testConfig(t, false))
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
	cp, sandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime", "toby", "context"), testConfig(t, true))
	var got []string
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}

	if err := cp.PrepareHost(context.Background(), &tools.Options{}); err != nil {
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
	plain, plainSandbox, _ := newTestCopilot(t, filepath.Join(home, "runtime2", "toby", "context"), testConfig(t, false))
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

func newTestCopilot(t *testing.T, contextDir string, cfg *appconfig.Service) (tools.Tool, *fake.Sandbox, *sessionconfig.Holder) {
	t.Helper()
	sandbox := fake.NewSandbox(contextDir)
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	holder := sessionconfig.NewHolder()
	tool := Provide(Params{Sandbox: sandbox, ContextFiles: contextFiles, SessionConfig: holder, Config: cfg}).Service
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

func hasFile(files []contextfiles.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}
