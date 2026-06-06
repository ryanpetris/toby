package opencode

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/config/session"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestOpenCodeSetsSyntheticConfigDir(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	var oc tools.Tool
	app := fxtest.New(t,
		fx.Supply(paths),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Supply(contextFiles),
		fx.Provide(func() *http.Client { return &http.Client{} }),
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
	want := filepath.Join(layout.Context, "opencode")
	if sandbox.Env["OPENCODE_CONFIG_DIR"] != want {
		t.Fatalf("OPENCODE_CONFIG_DIR = %q, want %q", sandbox.Env["OPENCODE_CONFIG_DIR"], want)
	}
}

func TestOpenCodeDeclaresNPMDependency(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	contextFiles := contextfiles.NewService()
	contextFiles.SetSandbox(sandbox)
	oc := Provide(Params{

		Sandbox:      sandbox,
		ContextFiles: contextFiles,
	}).Service

	if got := oc.Dependencies(); len(got) != 1 || got[0] != npm.Name {
		t.Fatalf("dependency metadata = deps %#v", got)
	}
}

func TestOpenCodeHostInitRegistersManagedMounts(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Sandbox: sandbox}).Service
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
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "runtime", "toby", "context"))
	oc := Provide(Params{Sandbox: sandbox}).Service
	if err := oc.PrepareHost(context.Background(), &tools.Options{}); err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Mounts) != 2 {
		t.Fatalf("mounts = %#v", sandbox.Mounts)
	}
}
