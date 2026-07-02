package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/internal/client"
	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/tools/wiring"
	"petris.dev/toby/tools"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestRootCommandWiresRequiredServicesThroughFx(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes")}
	var cmd *cobra.Command
	app := fxtest.New(t,
		wiring.PlanningModule(),
		tools.Module(),
		transportModule(),
		client.Module(),
		fx.Supply(paths, args(nil)),
		fx.Provide(
			appconfig.New,
			newClientSessionRunner,
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
	if err := os.WriteFile(configPath, []byte("bogus: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app := fx.New(
		fx.NopLogger,
		wiring.PlanningModule(),
		tools.Module(),
		transportModule(),
		client.Module(),
		fx.Supply(paths, args([]string{"--help"})),
		fx.Provide(
			appconfig.New,
			newClientSessionRunner,
			newRootCommand,
			newCLIResult,
		),
		fx.Invoke(runCLI),
	)

	if code := runApp(app, newCLIResult(), &stderr); code == 0 {
		t.Fatal("expected invalid config to fail")
	}
	got := strings.TrimSpace(stderr.String())
	if !strings.Contains(got, `unknown field "bogus"`) {
		t.Fatalf("stderr = %q, want unknown-field error", got)
	}
}

func TestModuleDependencyGraphIsValid(t *testing.T) {
	if err := fx.ValidateApp(Module()); err != nil {
		t.Fatal(err)
	}
}
