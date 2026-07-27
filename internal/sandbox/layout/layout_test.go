package layout

// Verifies the stable sandbox layout and private-home expansion.

import "testing"

func TestExpandHome(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{"~", Home},
		{"~/.config", Home + "/.config"},
		{"/tmp/~", "/tmp/~"},
	} {
		if got := ExpandHome(tt.input); got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSandboxBinary(t *testing.T) {
	if got, want := SandboxBinary(), "/toby/bin/tobys"; got != want {
		t.Fatalf("SandboxBinary = %q, want %q", got, want)
	}
}

func TestSandboxSocket(t *testing.T) {
	if got, want := SandboxSocket(), "/run/toby/sandbox.sock"; got != want {
		t.Fatalf("SandboxSocket = %q, want %q", got, want)
	}
}
