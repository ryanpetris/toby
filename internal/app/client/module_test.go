package client

// Verifies root application composition, CLI lifecycle behavior, and lazy
// native-runtime graph construction.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"

	"github.com/spf13/cobra"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestRootCommandWiresRequiredServicesThroughFx(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{
		Home:          home,
		XDGConfigHome: filepath.Join(home, ".config"),
		XDGCacheHome:  filepath.Join(home, ".cache"),
		XDGDataHome:   filepath.Join(home, ".local", "share"),
		ProjectRoot:   filepath.Join(home, "Projects"),
	}
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))

	var cmd *cobra.Command
	app := fxtest.New(t,
		processModule(),
		fx.Replace(paths, args(nil)),
		fx.Populate(&cmd),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	if cmd == nil {
		t.Fatal("root command was not wired")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		paths.RunStorageDir(),
		filepath.Join(paths.TobyDataDir(), "images"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf(
				"help eagerly created native runtime path %q: %v",
				path,
				err,
			)
		}
	}
}

func TestRunAppHelpDoesNotReadInvalidConfig(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects")}
	configDir := paths.TobyConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("bogus: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	var result *cliResult
	app := fx.New(
		processModule(),
		fx.Replace(paths, args([]string{"--help"})),
		fx.Invoke(runCLI),
		fx.Populate(&result),
	)

	if code := runApp(app, result, &stderr); code != 0 {
		t.Fatalf("help exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("help stderr = %q, want empty", got)
	}
}

func TestConflictingOutputModesDoNotReadInvalidConfig(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{
		Home:          home,
		XDGConfigHome: filepath.Join(home, ".config"),
		ProjectRoot:   filepath.Join(home, "Projects"),
	}
	configDir := paths.TobyConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(
		configPath,
		[]byte("bogus: true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var base *appconfig.Service
	graph := fx.New(
		processModule(),
		fx.Replace(
			paths,
			args{"codex", "project", "--debug", "--quiet"},
		),
		fx.Populate(&base),
	)
	if err := graph.Err(); err != nil {
		t.Fatalf("construct config-free conflict graph: %v", err)
	}
	if base == nil {
		t.Fatal("base config service was not constructed")
	}
	if base.Settings().DebugEnabled() {
		t.Fatal("conflicting output modes loaded the invalid host config")
	}
}

func TestModuleDependencyGraphIsValid(t *testing.T) {
	configureIsolatedGraphEnvironment(t)

	if err := fx.ValidateApp(Module()); err != nil {
		t.Fatal(err)
	}
}

func TestModuleCreatesNoNativeStateDuringComposition(
	t *testing.T,
) {
	base := configureIsolatedGraphEnvironment(t)

	app := fx.New(Module())
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(base, "cache", "toby", "runs"),
		filepath.Join(base, "data", "toby", "images"),
		filepath.Join(base, "runtime", "toby"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf(
				"application graph eagerly created %q: %v",
				path,
				err,
			)
		}
	}
}

func configureIsolatedGraphEnvironment(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_PROJECTS_DIR", filepath.Join(base, "projects"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("PATH", filepath.Join(base, "empty-bin"))

	return base
}
