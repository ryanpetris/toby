package configwatch

import (
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/config"
	appconfig "petris.dev/toby/internal/config/app"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherReloadsOnChangeAndKeepsLastGood(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "toby")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, XDGConfigHome: filepath.Join(home, ".config")}

	writeConfig(t, cfgDir, "container:\n  image: image-a\n")
	w, err := New(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Current().Image(); got != "image-a" {
		t.Fatalf("initial image = %q, want image-a", got)
	}

	// A change is picked up.
	var reloaded bool
	w.SetOnReload(func(*appconfig.Service) { reloaded = true })
	writeConfig(t, cfgDir, "container:\n  image: image-b\n")
	if !w.reloadIfChanged() {
		t.Fatal("expected reload on change")
	}
	if got := w.Current().Image(); got != "image-b" {
		t.Fatalf("reloaded image = %q, want image-b", got)
	}
	if !reloaded {
		t.Fatal("onReload not invoked")
	}

	// A broken edit keeps the last-good config.
	writeConfig(t, cfgDir, "container: [this is not valid: {\n")
	w.reloadIfChanged()
	if got := w.Current().Image(); got != "image-b" {
		t.Fatalf("after bad edit image = %q, want image-b (last good)", got)
	}
}
