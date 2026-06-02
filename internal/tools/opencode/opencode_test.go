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
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
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

			Tools []tool.Tool `group:"toby.tools"`
		}) {
			for _, item := range params.Tools {
				if item.Name() == tool.OpenCodeToolName {
					oc = item
				}
			}
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

func TestOpenCodeDeclaresNPMDependency(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	oc := Provide(Params{
		Paths:        paths,
		Sandbox:      sandbox,
		ContextFiles: contextFiles,
	}).Service

	if got := oc.Dependencies(); len(got) != 1 || got[0] != tool.NpmToolName || oc.LifecyclePriority() != 100 {
		t.Fatalf("dependency metadata = deps %#v priority %d", got, oc.LifecyclePriority())
	}
}

func TestOpenCodeHostInitRegistersManagedMounts(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Paths: paths, Sandbox: sandbox}).Service
	if err := oc.HostInit(context.Background(), &tool.CommandOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("managed mounts registered binds: %#v", sandbox.Binds)
	}
	want := []struct {
		key    sandboxmount.Key
		target string
	}{
		{sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "config"}, filepath.Join(sandbox.Paths().Home, ".config", "opencode")},
		{sandboxmount.Key{Type: sandboxmount.TypeTool, Name: tool.OpenCodeToolName, Purpose: "data"}, filepath.Join(sandbox.Paths().Home, ".local", "share", "opencode")},
	}
	if len(sandbox.Mounts) != len(want) {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
	for i, item := range want {
		if sandbox.Mounts[i].Key != item.key || sandbox.Mounts[i].Target != item.target {
			t.Fatalf("mount[%d] = %#v, want %#v", i, sandbox.Mounts[i], item)
		}
	}
}

func TestOpenCodeHostInitOnce(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Paths: paths, Sandbox: sandbox}).Service
	opts := &tool.CommandOptions{}
	if err := oc.HostInit(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := oc.HostInit(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Mounts) != 2 {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
}

func TestOpenCodeModelDiscoveryWarningUsesIDAndSuppression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	home := t.TempDir()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(fmt.Sprintf(`{"providers":{"local":{"type":"openai","baseURL":%q}}}`, server.URL)), 0o600); err != nil {
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
	oc := Provide(Params{Paths: paths, Renderer: renderer, Config: cfg, Proxy: httpproxy.NewService(httpproxy.ServiceParams{}), Sandbox: sandbox, ContextFiles: service}).Service.(tool.ContextFileTool)

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
