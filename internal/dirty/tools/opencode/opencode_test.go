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

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/internal/dirty/tools/npm"
	opencodeconfig "petris.dev/toby/internal/dirty/tools/opencode/config"
	"petris.dev/toby/providers"
	"petris.dev/toby/providers/openai"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/tooltest"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type fakeNPM struct {
	tools.Base
	sandbox sandboxapi.Service
}

func (t fakeNPM) ConfigureSandbox(ctx context.Context) error {
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
	var oc tools.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Supply(contextFiles),
		fx.Provide(func() *http.Client { return &http.Client{} }),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"toby.tools"`
		}) {
			for _, item := range params.Tools {
				if item.Name() == tools.OpenCodeToolName {
					oc = item
				}
			}
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if err := oc.ConfigureSandbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(layout.Context, "opencode")
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

	if got := oc.Dependencies(); len(got) != 1 || got[0] != tools.NpmToolName || oc.LifecyclePriority() != 100 {
		t.Fatalf("dependency metadata = deps %#v priority %d", got, oc.LifecyclePriority())
	}
}

func TestOpenCodeHostInitRegistersManagedMounts(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Paths: paths, Sandbox: sandbox}).Service
	if err := oc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Binds) != 0 {
		t.Fatalf("managed mounts registered binds: %#v", sandbox.Binds)
	}
	want := []struct {
		key    mount.Key
		target string
	}{
		{mount.Key{Type: mount.TypeTool, Name: tools.OpenCodeToolName, Purpose: "config"}, filepath.Join(layout.Home, ".config", "opencode")},
		{mount.Key{Type: mount.TypeTool, Name: tools.OpenCodeToolName, Purpose: "data"}, filepath.Join(layout.Home, ".local", "share", "opencode")},
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

func TestOpenCodeHostPrepareAddsMounts(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Paths: paths, Sandbox: sandbox}).Service
	if err := oc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
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
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(fmt.Sprintf(`{"provider":{"local":{"type":"openai","baseURL":%q}}}`, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(cfgDir, home)
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry([]providers.Client{openai.New(server.Client())})
	renderer, err := opencodeconfig.NewRenderer(registry)
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := tooltest.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	sandbox.MCPURL = "http://127.0.0.1:12345/proxy/toby"
	sandbox.Env[control.EnvControlHost] = "127.0.0.1:12345"
	service := contextfiles.NewService()
	service.SetSandbox(sandbox)
	oc := Provide(Params{Paths: paths, Renderer: renderer, Config: cfg, Proxy: httpproxy.NewService(nil), Sandbox: sandbox, ContextFiles: service}).Service.(tools.ContextFileRegistrar)

	var stderr bytes.Buffer
	if err := oc.RegisterContextFiles(context.Background(), tools.ContextOptions{Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, "warning[opencode.model-discovery]") {
		t.Fatalf("warning = %q", got)
	}

	stderr.Reset()
	if err := oc.RegisterContextFiles(context.Background(), tools.ContextOptions{Stderr: &stderr, SuppressWarnings: warning.Suppression{Set: true, IDs: map[warning.ID]bool{warning.OpenCodeModelDiscovery: true}}}); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("suppressed warning = %q", stderr.String())
	}
}
