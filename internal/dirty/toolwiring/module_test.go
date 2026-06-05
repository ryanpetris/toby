package toolwiring

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"petris.dev/toby/config"
	contextfiles "petris.dev/toby/context/files"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/sessionconfig"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/fake"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestModuleRegistersEveryConfiguredTool(t *testing.T) {
	home := t.TempDir()
	sandbox := fake.NewSandbox(filepath.Join(home, "context"))
	var registered []string
	registeredTools := map[string]tools.Tool{}
	app := fxtest.New(t,
		fx.Supply(config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Provide(contextfiles.NewService, sessionconfig.NewHolder),
		Module(),
		fx.Invoke(func(params struct {
			fx.In

			Tools []tools.Tool `group:"tools"`
		}) {
			for _, item := range params.Tools {
				registered = append(registered, item.Name())
				registeredTools[item.Name()] = item
			}
		}),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)

	want := configuredToolNames()
	sort.Strings(registered)
	if !reflect.DeepEqual(registered, want) {
		t.Fatalf("registered tools = %#v, want %#v", registered, want)
	}
	seen := map[string]bool{}
	for _, name := range registered {
		if name == "" {
			t.Fatal("registered tool with empty name")
		}
		if seen[name] {
			t.Fatalf("duplicate tool registration: %s", name)
		}
		seen[name] = true
	}
	for _, metadata := range Metadata() {
		registeredTool := registeredTools[metadata.Name]
		if registeredTool == nil {
			continue
		}
		expected := tools.Base{Metadata: metadata}
		if registeredTool.CommandName() != expected.CommandName() || registeredTool.LaunchHelp() != expected.LaunchHelp() || registeredTool.LifecyclePriority() != expected.LifecyclePriority() {
			t.Fatalf("metadata mismatch for %s", metadata.Name)
		}
		if !reflect.DeepEqual(registeredTool.ContextGroups(), expected.ContextGroups()) || !reflect.DeepEqual(registeredTool.Dependencies(), expected.Dependencies()) {
			t.Fatalf("metadata mismatch for %s", metadata.Name)
		}
	}
}

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
	module, err := SelectedModule([]string{tools.OpenCodeToolName, tools.NpmToolName, tools.OpenCodeToolName})
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
	want := []string{tools.NpmToolName, tools.OpenCodeToolName}
	if !reflect.DeepEqual(registered, want) {
		t.Fatalf("selected tools = %#v, want %#v", registered, want)
	}
}

func TestMetadataAndSelectedModulesCoverConfiguredTools(t *testing.T) {
	metadataNames := make([]string, 0, len(Metadata()))
	for _, metadata := range Metadata() {
		metadataNames = append(metadataNames, metadata.Name)
	}
	sort.Strings(metadataNames)

	moduleNames := make([]string, 0, len(toolModules))
	for name := range toolModules {
		moduleNames = append(moduleNames, name)
	}
	sort.Strings(moduleNames)

	want := configuredToolNames()
	if !reflect.DeepEqual(metadataNames, want) {
		t.Fatalf("metadata tools = %#v, want %#v", metadataNames, want)
	}
	if !reflect.DeepEqual(moduleNames, want) {
		t.Fatalf("selected modules = %#v, want %#v", moduleNames, want)
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
