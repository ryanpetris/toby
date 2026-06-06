package app

import (
	"io"
	"path/filepath"
	"testing"

	"go.uber.org/fx"

	"petris.dev/toby/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/tools/wiring"
)

// TestSessionGraphIsValid validates the per-launch fx graph (where the
// session-config resolver, provider registry, and selected tools are wired)
// resolves run.Params. It checks both an opencode launch (exercises the
// provider path) and a no-tool launch (the provider registry is always on).
func TestSessionGraphIsValid(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes")}
	cases := map[string][]string{
		"opencode": {"opencode"},
		"no-tools": nil,
	}
	for name, selected := range cases {
		t.Run(name, func(t *testing.T) {
			toolModule, err := wiring.SelectedModule(selected)
			if err != nil {
				t.Fatal(err)
			}
			options := append(sessionModules(toolModule, io.Discard),
				fx.Supply(paths),
				fx.Provide(appconfig.New),
				fx.Invoke(func(run.Params) {}),
			)
			if err := fx.ValidateApp(options...); err != nil {
				t.Fatal(err)
			}
		})
	}
}
