package mount

// Verifies native mount identity, path validation, and overlap semantics.

import "testing"

func TestKeyValidation(t *testing.T) {
	valid := Key{Type: TypeTool, Name: "open-code", Purpose: "state.v2"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, key := range []Key{
		{Type: "", Name: "x", Purpose: "y"},
		{Type: "provider", Name: "x", Purpose: "y"},
		{Type: TypeTool, Name: "..", Purpose: "y"},
		{Type: TypeTool, Name: "a/b", Purpose: "y"},
		{Type: TypeTool, Name: " x", Purpose: "y"},
		{Type: TypeTool, Name: "x", Purpose: ".toby-tmp-0123456789abcdef0123456789abcdef"},
	} {
		if err := key.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", key)
		}
	}
}

func TestEntryValidation(t *testing.T) {
	entry := Entry{
		Key:      Key{Type: TypeTool, Name: "opencode", Purpose: "state"},
		Profile:  "default",
		HostPath: "/data/toby/volumes/abc/_data",
		Target:   "/toby/home/.local/share/opencode",
		Access:   AccessRegular,
		Seed:     Seed{ImagePath: "/toby/home/.local/share/opencode"},
	}
	if err := entry.Validate(); err != nil {
		t.Fatal(err)
	}

	entry.Target = "/toby/home/../escape"
	if err := entry.Validate(); err == nil {
		t.Fatal("unclean target was accepted")
	}

	entry.Target = "/toby/home/.local/share/opencode"
	for _, hostPath := range []string{"/data/../escape", "/data/\x00escape"} {
		entry.HostPath = hostPath
		if err := entry.Validate(); err == nil {
			t.Fatalf("invalid host path %q was accepted", hostPath)
		}
	}
}

func TestBindValidation(t *testing.T) {
	bind := Bind{HostPath: "/run/user/1000/docker.sock", Target: "/run/toby/docker.sock", Access: AccessDev}
	if err := bind.Validate(); err != nil {
		t.Fatal(err)
	}

	bind.HostPath = "relative"
	if err := bind.Validate(); err == nil {
		t.Fatal("relative host path was accepted")
	}

	for _, hostPath := range []string{"/run/../escape", "/run/\x00escape"} {
		bind.HostPath = hostPath
		if err := bind.Validate(); err == nil {
			t.Fatalf("invalid host path %q was accepted", hostPath)
		}
	}
}

func TestTargetsOverlap(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"/a", "/a", true},
		{"/a", "/a/b", true},
		{"/a/b", "/a", true},
		{"/a", "/ab", false},
		{"/a/b", "/a/c", false},
		{"/", "/a", true},
	} {
		if got := TargetsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("TargetsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
