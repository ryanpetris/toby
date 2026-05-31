package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/context/setup"
	"petris.dev/toby/internal/control/hostmanager"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/control/sandboxmanager"
	"petris.dev/toby/internal/platform/executil"
	"petris.dev/toby/internal/sandbox"
	sandboxbubblewrap "petris.dev/toby/internal/sandbox/bubblewrap"
	sandboxdocker "petris.dev/toby/internal/sandbox/docker"
	"petris.dev/toby/internal/tools/tool"

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
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes")}
	var cmd *cobra.Command
	app := fxtest.New(t,
		hostmanager.Module(),
		mcpserver.Module(),
		sandbox.Module(),
		sandboxbubblewrap.Module(),
		sandboxdocker.Module(),
		sandboxmanager.Module(),
		fx.Supply(paths, args(nil)),
		fx.Provide(
			func() executil.Runner { return fakeRunner{} },
			contextfiles.NewService,
			tobyconfig.New,
			contextinit.NewServices,
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

func TestRunAppReportsInvalidConfig(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes")}
	configDir := paths.TobyConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("sandbox:\n  invalid: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app := fx.New(
		fx.NopLogger,
		hostmanager.Module(),
		mcpserver.Module(),
		sandbox.Module(),
		sandboxbubblewrap.Module(),
		sandboxdocker.Module(),
		sandboxmanager.Module(),
		fx.Supply(paths, args([]string{"--help"})),
		fx.Provide(
			func() executil.Runner { return fakeRunner{} },
			contextfiles.NewService,
			tobyconfig.New,
			contextinit.NewServices,
			tool.NewRegistry,
			newRootCommand,
		),
		fx.Invoke(runCLI),
	)

	if code := runApp(app, &stderr); code == 0 {
		t.Fatal("expected invalid config to fail")
	}
	got := strings.TrimSpace(stderr.String())
	want := configPath + `: unsupported sandbox key "invalid"`
	if got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestModuleDependencyGraphIsValid(t *testing.T) {
	if err := fx.ValidateApp(Module()); err != nil {
		t.Fatal(err)
	}
}
