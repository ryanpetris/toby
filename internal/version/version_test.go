package version

// Tests build-time and module-derived version reporting.

import (
	"runtime/debug"
	"testing"
)

func TestStringUsesInjectedVersion(t *testing.T) {
	setVersionForTest(t, "v1.2.3")

	if got := String(); got != "v1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3")
	}
}

func TestResolveUsesModuleVersionWhenVersionIsDev(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.2.3"},
	}
	if got := resolve("dev", info, true); got != "v1.2.3" {
		t.Fatalf("resolve() = %q, want %q", got, "v1.2.3")
	}
}

func TestResolveDefaultsToDev(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}
	if got := resolve("", info, true); got != "dev" {
		t.Fatalf("resolve() = %q, want %q", got, "dev")
	}
}

func setVersionForTest(t *testing.T, value string) {
	t.Helper()
	old := Current
	Current = value
	t.Cleanup(func() { Current = old })
}
