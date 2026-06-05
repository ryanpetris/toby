package app

import (
	"io"
	"path/filepath"
	"testing"

	"go.uber.org/fx"

	"petris.dev/toby/config"
	tobyconfig "petris.dev/toby/config/toby"
	"petris.dev/toby/internal/dirty/cli/session"
	sandboxdocker "petris.dev/toby/internal/dirty/sandbox/docker"
	"petris.dev/toby/internal/dirty/toolwiring"
	"petris.dev/toby/tools"
)

// TestSessionGraphIsValid validates the per-launch fx graph (where the
// session-config resolver, provider registry, and selected tools are wired)
// resolves session.Params. It checks both an opencode launch (exercises the
// provider path) and a no-tool launch (the provider registry is always on).
func TestSessionGraphIsValid(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config"), ProjectRoot: filepath.Join(home, "Projects"), SandboxRoot: filepath.Join(home, "sandboxes")}
	cases := map[string][]string{
		"opencode": {tools.OpenCodeToolName},
		"no-tools": nil,
	}
	for name, selected := range cases {
		t.Run(name, func(t *testing.T) {
			toolModule, err := toolwiring.SelectedModule(selected)
			if err != nil {
				t.Fatal(err)
			}
			options := append(sessionModules(toolModule, sandboxdocker.Module(), io.Discard),
				fx.Supply(paths),
				fx.Provide(tobyconfig.New),
				fx.Invoke(func(session.Params) {}),
			)
			if err := fx.ValidateApp(options...); err != nil {
				t.Fatal(err)
			}
		})
	}
}
