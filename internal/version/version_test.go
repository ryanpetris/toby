package version

import (
	"runtime/debug"
	"testing"
)

func TestStringUsesInjectedVersion(t *testing.T) {
	setVersionForTest(t, "v1.2.3")
	setBuildInfoForTest(t, "v1.0.0")

	if got := String(); got != "v1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3")
	}
}

func TestStringUsesModuleVersionWhenVersionIsDev(t *testing.T) {
	setVersionForTest(t, "dev")
	setBuildInfoForTest(t, "v1.2.3")

	if got := String(); got != "v1.2.3" {
		t.Fatalf("String() = %q, want %q", got, "v1.2.3")
	}
}

func TestStringDefaultsToDev(t *testing.T) {
	setVersionForTest(t, "")
	setBuildInfoForTest(t, "(devel)")

	if got := String(); got != "dev" {
		t.Fatalf("String() = %q, want %q", got, "dev")
	}
}

func setVersionForTest(t *testing.T, value string) {
	t.Helper()
	old := Current
	Current = value
	t.Cleanup(func() { Current = old })
}

func setBuildInfoForTest(t *testing.T, mainVersion string) {
	t.Helper()
	old := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: mainVersion}}, true
	}
	t.Cleanup(func() { readBuildInfo = old })
}
