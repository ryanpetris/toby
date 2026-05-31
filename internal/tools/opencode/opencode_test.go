package opencode

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/tools/npm"
	opencodeconfig "petris.dev/toby/internal/tools/opencode/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/tooltest"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type fakeNPM struct {
	tool.Base
	sandbox tool.SandboxService
}

func (fakeNPM) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.AbsoluteTarget("/npm/bin")}
}

func (t fakeNPM) SandboxContextSetup(ctx context.Context) error {
	if t.sandbox == nil {
		return nil
	}
	if err := t.sandbox.SetEnvironment(ctx, "NPM_CALLED", "1"); err != nil {
		return err
	}
	return t.sandbox.SetEnvironment(ctx, "OPENCODE_CONFIG_DIR", "dependency")
}

func TestOpenCodeSetsSyntheticConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	var oc tool.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(tool.SandboxService)))),
		fx.Supply(contextFiles),
		fx.Provide(func() *http.Client { return &http.Client{} }),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			OpenCode tool.Tool `name:"opencode"`
		}) {
			oc = params.OpenCode
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if err := oc.SandboxContextSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "runtime", "toby", "context", "opencode")
	if sandbox.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", sandbox.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodeCallsDependencyBeforeOwnContextSetup(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	oc := Provide(Params{
		Paths:        paths,
		NPM:          fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}, sandbox: sandbox},
		Sandbox:      sandbox,
		ContextFiles: contextFiles,
	}).Service

	if got, want := oc.PathEntries(), []tool.PathTarget{tool.AbsoluteTarget("/npm/bin")}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("PathEntries = %#v, want %#v", got, want)
	}
	if err := oc.SandboxContextSetup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sandbox.Env["NPM_CALLED"] != "1" {
		t.Fatalf("dependency SandboxContextSetup was not called")
	}
	want := filepath.Join(home, "runtime", "toby", "context", "opencode")
	if sandbox.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", sandbox.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodePrivateStateDoesNotBindHostState(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	called := false
	npm := hostInitNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}, called: &called, sandboxRoot: paths.SandboxRoot}
	oc := Provide(Params{Paths: paths, NPM: npm}).Service
	if err := oc.HostInit(context.Background(), &tool.CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatalf("dependency HostInit was not called")
	}
	for _, dir := range []string{
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".local", "share", "opencode"),
	} {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("private state created host dir %s", dir)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for _, bind := range oc.Binds() {
		if !bind.State {
			continue
		}
		if bind.HostPath == "" || !filepath.IsAbs(bind.HostPath) {
			t.Fatalf("state bind = %#v", bind)
		}
	}
}

func TestOpenCodeHostStateCreatesHostDirs(t *testing.T) {
	home := t.TempDir()
	stateRoot := filepath.Join(home, "state-root")
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	oc := Provide(Params{Paths: paths, NPM: fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}}}).Service
	opts := &tool.CommandOptions{ToolStates: tool.ToolStateSettings{Default: tool.ToolStateConfig{StateRoot: home}, Tools: map[string]tool.ToolStateConfig{tool.OpenCodeToolName: {State: tool.ToolStateHost, StateRoot: stateRoot}}}}
	if err := oc.HostInit(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(stateRoot, ".config", "opencode"),
		filepath.Join(stateRoot, ".local", "share", "opencode"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(stateRoot, ".opencode")); err == nil {
		t.Fatal("opencode should not create .opencode")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	registry, err := tool.NewRegistry(tool.RegistryParams{Tools: []tool.Tool{oc}})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{tool.OpenCodeToolName}, "")
	if err != nil {
		t.Fatal(err)
	}
	toolset.SetToolStates(opts.ToolStates)
	binds := toolset.Binds()
	want := []string{filepath.Join(stateRoot, ".config", "opencode"), filepath.Join(stateRoot, ".local", "share", "opencode")}
	if len(binds) != len(want) {
		t.Fatalf("binds = %#v", binds)
	}
	for i, bind := range binds {
		if bind.HostPath != want[i] {
			t.Fatalf("bind[%d] = %#v, want host %q", i, bind, want[i])
		}
	}
}

func TestOpenCodeModelDiscoveryWarningUsesIDAndSuppression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	home := t.TempDir()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(fmt.Sprintf(`{"provider":{"local":{"type":"openai","baseURL":%q}}}`, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(cfgDir, home)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := opencodeconfig.NewRenderer(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	sandbox.Env[control.EnvControlHost] = "127.0.0.1:12345"
	service := contextfiles.NewService()
	service.SetSandbox(sandbox)
	oc := Provide(Params{Paths: paths, NPM: fakeNPM{Base: tool.Base{Metadata: tool.Metadata{Name: tool.NpmToolName}}}, Renderer: renderer, Config: cfg, Proxy: httpproxy.NewService(httpproxy.ServiceParams{}), Sandbox: sandbox, ContextFiles: service}).Service.(tool.ContextFileTool)

	var stderr bytes.Buffer
	if err := oc.RegisterContextFiles(context.Background(), tool.ContextOptions{Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, "warning[opencode.model-discovery]") {
		t.Fatalf("warning = %q", got)
	}

	stderr.Reset()
	if err := oc.RegisterContextFiles(context.Background(), tool.ContextOptions{Stderr: &stderr, SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.OpenCodeModelDiscovery: true}}}); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed warning = %q", stderr.String())
	}
}

type hostInitNPM struct {
	tool.Base
	called      *bool
	sandboxRoot string
}

func (t hostInitNPM) HostInit(context.Context, *tool.CommandOptions) error {
	if _, err := os.Stat(filepath.Join(t.sandboxRoot, ".config", "opencode")); err == nil {
		return fmt.Errorf("opencode HostInit ran before dependency HostInit")
	} else if !os.IsNotExist(err) {
		return err
	}
	*t.called = true
	return nil
}
