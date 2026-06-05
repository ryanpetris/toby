package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/control/sandbox"
	"petris.dev/toby/internal/dirty/toolwiring"
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
		sandbox.Module(),
		toolwiring.PlanningModule(),
		tools.Module(),
		fx.Supply(paths, args(nil)),
		fx.Provide(
			tobyconfig.New,
			newSessionRunner,
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
		sandbox.Module(),
		toolwiring.PlanningModule(),
		tools.Module(),
		fx.Supply(paths, args([]string{"--help"})),
		fx.Provide(
			tobyconfig.New,
			newSessionRunner,
			newRootCommand,
		),
		fx.Invoke(runCLI),
	)

	if code := runApp(app, &stderr); code == 0 {
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
