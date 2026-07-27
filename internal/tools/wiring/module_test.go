package wiring

// Tests built-in tool metadata and Fx module registration.

import (
	"reflect"
	"sort"
	"testing"

	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/fake"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestModuleRegistersEveryConcreteToolOnce(t *testing.T) {
	base, err := appconfig.Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	sandbox := fake.NewSandbox()
	var registered []string
	app := fxtest.New(t,
		fx.Supply(config.Paths{Home: home}),
		fx.Supply(fx.Annotate(sandbox, fx.As(new(sandboxapi.Service)))),
		fx.Supply(appconfig.NewLaunchHolder(base)),
		fx.Provide(sessionconfig.NewHolder),
		diagnostic.Module(),
		Module,
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
		t.Fatalf("registered tools = %#v, want %#v", registered, want)
	}
}

func TestBuiltInToolsDefineDisplayNames(t *testing.T) {
	for _, metadata := range registeredMetadata() {
		if metadata.DisplayName == "" {
			t.Errorf("tool %q has no display name", metadata.Name)
		}
	}
}

func configuredToolNames() []string {
	metadatas := registeredMetadata()
	names := make([]string, 0, len(metadatas))
	for _, metadata := range metadatas {
		names = append(names, metadata.Name)
	}
	sort.Strings(names)
	return names
}

func registeredMetadata() []tools.Metadata {
	result := make([]tools.Metadata, len(entries))
	for index, entry := range entries {
		metadata := entry.Meta
		metadata.ContextGroups = append(
			[]string(nil),
			entry.Meta.ContextGroups...,
		)
		metadata.Dependencies = append(
			[]string(nil),
			entry.Meta.Dependencies...,
		)
		result[index] = metadata
	}

	return result
}
