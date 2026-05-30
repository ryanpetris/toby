package tools

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestModuleRegistersEveryConfiguredTool(t *testing.T) {
	home := t.TempDir()
	var registered []string
	app := fxtest.New(t,
		fx.Supply(config.Paths{Home: home, SandboxRoot: filepath.Join(home, "sandboxes")}),
		Module(),
		fx.Invoke(func(params struct {
			fx.In

			Tools []tool.Tool `group:"toby.tools"`
		}) {
			for _, item := range params.Tools {
				registered = append(registered, item.Name())
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
}

func configuredToolNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, groupNames := range tool.ToolGroups {
		for _, name := range groupNames {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
