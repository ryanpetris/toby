package executable

// Verifies the closed companion-binary name set.

import "testing"

func TestResolveRejectsUnknownExecutable(t *testing.T) {
	if _, err := Resolve("other"); err == nil {
		t.Fatal("unknown executable was accepted")
	}
}
