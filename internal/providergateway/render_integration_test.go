//go:build linux

package providergateway

// Validates the production native-JSON schema against an installed Caddy when
// the explicit integration environment is enabled.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRenderedConfigAcceptedByCaddy(t *testing.T) {
	if os.Getenv("TOBY_CADDY_INTEGRATION") != "1" {
		t.Skip(
			"set TOBY_CADDY_INTEGRATION=1 with Caddy installed",
		)
	}

	caddyPath, err := exec.LookPath("caddy")
	if err != nil {
		t.Fatal("find Caddy:", err)
	}
	item := testRoute(
		"route-one",
		"cap-one",
		"fake-one",
	)
	item.Provider.Headers = map[string]string{
		"Authorization": "Bearer real-secret",
	}
	config, err := renderCaddyConfig(
		routeSnapshot{Routes: []route{item}},
		"generation-token",
	)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "caddy.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(
		caddyPath,
		"validate",
		"--config",
		path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf(
			"Caddy rejected production JSON: %v: %s",
			err,
			output,
		)
	}
}
