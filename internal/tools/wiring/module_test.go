package wiring

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/builtin/opencode"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestPlanningModuleRegistersEveryConfiguredToolWithoutExecutionServices(t *testing.T) {
	var registered []string
	app := fxtest.New(t,
		PlanningModule(),
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"tools"`
		}) {
			for _, item := range params.Tools {
				registered = append(registered, item.Name())
			}
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	sort.Strings(registered)
	if want := configuredToolNames(); !reflect.DeepEqual(registered, want) {
		t.Fatalf("planning tools = %#v, want %#v", registered, want)
	}
}

func TestSelectedModuleRegistersOnlySelectedTools(t *testing.T) {
	module, err := SelectedModule([]string{opencode.Name, npm.Name, opencode.Name})
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "context"))
	var registered []string
	app := fxtest.New(t,
		fx.Supply(config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Provide(contextfiles.NewService, sessionconfig.NewHolder),
		module,
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"tools"`
		}) {
			for _, item := range params.Tools {
				registered = append(registered, item.Name())
			}
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	sort.Strings(registered)
	want := []string{npm.Name, opencode.Name}
	if !reflect.DeepEqual(registered, want) {
		t.Fatalf("selected tools = %#v, want %#v", registered, want)
	}
}

func configuredToolNames() []string {
	metadatas := Metadata()
	names := make([]string, 0, len(metadatas))
	for _, metadata := range metadatas {
		names = append(names, metadata.Name)
	}
	sort.Strings(names)
	return names
}
