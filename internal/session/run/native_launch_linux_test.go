//go:build linux

package run

// Verifies host-environment detection for payload terminal claims.

import "testing"

func TestNativePayloadClaimsTerminalDetectsHerdrPane(t *testing.T) {
	environment := map[string]string{"HERDR_PANE_ID": "pane-1"}
	lookup := func(name string) string {
		return environment[name]
	}
	if !nativePayloadClaimsTerminal(lookup) {
		t.Fatal("Herdr pane environment did not enable the terminal claim")
	}

	if nativePayloadClaimsTerminal(func(string) string { return "" }) {
		t.Fatal("empty environment enabled the terminal claim")
	}
}
