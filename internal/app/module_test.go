package app

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/executil"
	"petris.dev/toby/internal/opencodeconfig"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type fakeRunner struct{}

func (fakeRunner) Run(context.Context, []string, map[string]string, executil.Options) (int, error) {
	return 0, nil
}

func TestRootCommandWiresRequiredServicesThroughFx(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes"), XDGRuntimeDir: filepath.Join(home, "runtime")}
	var cmd *cobra.Command
	app := fxtest.New(t,
		fx.Supply(paths, args(nil)),
		fx.Provide(
			func() *http.Client { return &http.Client{} },
			func() executil.Runner { return fakeRunner{} },
			opencodeconfig.NewRenderer,
			sandbox.NewFactory,
			contextfiles.NewService,
			tobyconfig.New,
			tool.NewRegistry,
			newRootCommand,
		),
		fx.Populate(&cmd),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if cmd == nil {
		t.Fatal("root command was not wired")
	}
}

func TestModuleDependencyGraphIsValid(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "runtime"))
	if err := fx.ValidateApp(Module()); err != nil {
		t.Fatal(err)
	}
}
