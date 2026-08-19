package opencode

// Tests OpenCode's tool lifecycle declarations and runtime-specific behavior.

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/session"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/npm"
	opencodeconfig "petris.dev/toby/internal/tools/builtin/opencode/config"
	"petris.dev/toby/internal/tools/fake"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestOpenCodeInstallDescribesIntent(t *testing.T) {
	var got []string
	var installOptions sandboxapi.ExecOptions
	sandbox := fake.NewSandbox()
	sandbox.ExecFunc = func(
		_ context.Context,
		argv []string,
		options sandboxapi.ExecOptions,
	) (int, error) {
		if len(argv) != 0 && argv[0] == "which" {
			return 1, nil
		}
		got = append([]string(nil), argv...)
		installOptions = options
		return 0, nil
	}

	tool := provide(params{Sandbox: sandbox}).Service
	if err := tool.Install(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"npm", "install", "-g", "--allow-scripts=opencode-ai", "opencode-ai"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if installOptions.Status != "Installing" {
		t.Fatalf("install status = %q", installOptions.Status)
	}
}

func TestOpenCodeSetsNativeConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home}
	sandbox := fake.NewSandbox()
	var oc tools.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Provide(sessionconfig.NewHolder),
		npm.Module,
		Module,
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"tools"`
		}) {
			for _, item := range params.Tools {
				if item.Name() == Name {
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
	want := filepath.Dir(opencodeconfig.NativePriorityConfigPath)
	if sandbox.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", sandbox.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodeDeclaresNPMDependency(t *testing.T) {
	sandbox := fake.NewSandbox()
	oc := provide(params{
		Sandbox: sandbox,
	}).Service

	if got := oc.Dependencies(); len(got) != 1 || got[0] != npm.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}

func TestOpenCodeHostInitRegistersManagedMounts(t *testing.T) {
	sandbox := fake.NewSandbox()
	oc := provide(params{Sandbox: sandbox}).Service
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
		{mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "config"}, filepath.Join(layout.Home, ".config", "opencode")},
		{mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "data"}, filepath.Join(layout.Home, ".local", "share", "opencode")},
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
	sandbox := fake.NewSandbox()
	oc := provide(params{Sandbox: sandbox}).Service
	if err := oc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Mounts) != 2 {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
}

func TestOpenCodeNativeSandboxUsesDistinctHighestPriorityConfigDir(
	t *testing.T,
) {
	recorder := fake.NewSandbox()
	native := &nativeOpenCodeSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	holder.Set(sessionconfig.Config{
		MCPServers: []sessionconfig.MCPServer{{
			Name:      "toby",
			Transport: sessionconfig.MCPTransportHTTP,
			URL:       "http://127.0.0.1:12345/mcp/toby",
		}},
	})
	tool := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
	}).Service

	if err := tool.ConfigureSandbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Dir(opencodeconfig.NativePriorityConfigPath)
	if got := recorder.Env["OPENCODE_CONFIG_DIR"]; got != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", got, want)
	}
	if len(recorder.Env["OPENCODE_CONFIG_DIR"]) > 1024 {
		t.Fatalf("native config-dir environment is unexpectedly large")
	}
}

func TestOpenCodeLargeNativeConfigStaysOutOfEnvironment(t *testing.T) {
	recorder := fake.NewSandbox()
	native := &nativeOpenCodeSandbox{Sandbox: recorder}
	holder := sessionconfig.NewHolder()
	holder.Set(sessionconfig.Config{
		Models: []sessionconfig.ModelsEndpoint{{
			ID:   "large",
			Type: "openai",
			URL:  "http://127.0.0.1:12345/provider/large",
			Models: map[string]any{
				"large-model": map[string]any{
					"name": strings.Repeat("x", 140*1024),
				},
			},
		}},
	})
	tool := provide(params{
		Sandbox:       native,
		SessionConfig: holder,
	}).Service

	if err := tool.ConfigureSandbox(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, err := tool.(toolfiles.Contributor).ToolFiles(
		toolfiles.Ownership{UID: 1000, GID: 1000},
	)
	if err != nil {
		t.Fatal(err)
	}
	var configSize int
	for _, file := range files {
		if file.Target == opencodeconfig.NativePriorityConfigPath {
			configSize = len(file.Data)
		}
	}
	if configSize <= 128*1024 {
		t.Fatalf("large native config size = %d", configSize)
	}
	if got := recorder.Env["OPENCODE_CONFIG_DIR"]; got !=
		filepath.Dir(opencodeconfig.NativePriorityConfigPath) {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q", got)
	}
	for name, value := range recorder.Env {
		if len(value) > 1024 {
			t.Fatalf(
				"native environment %s unexpectedly carries %d bytes",
				name,
				len(value),
			)
		}
	}
}

type nativeOpenCodeSandbox struct {
	*fake.Sandbox
}

func (*nativeOpenCodeSandbox) UsesNativeToolFiles() bool {
	return true
}
