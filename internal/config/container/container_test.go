package containerconfig

import (
	"path/filepath"
	"testing"
)

func ptr(s string) *string { return &s }

func TestResolveBuildDefaults(t *testing.T) {
	build, err := ResolveBuild(&Build{}, "/cfg", "/home")
	if err != nil {
		t.Fatal(err)
	}
	if build.Context != "/cfg" {
		t.Fatalf("context = %q, want /cfg", build.Context)
	}
	if build.Dockerfile != filepath.Join("/cfg", "Dockerfile") {
		t.Fatalf("dockerfile = %q", build.Dockerfile)
	}
}

func TestResolveBuildNilIsUnset(t *testing.T) {
	build, err := ResolveBuild(nil, "/cfg", "/home")
	if err != nil {
		t.Fatal(err)
	}
	if build.IsSet() {
		t.Fatalf("nil build resolved to set: %#v", build)
	}
}

func TestResolveBuildExpandsHomeAndAnchors(t *testing.T) {
	build, err := ResolveBuild(&Build{Context: ptr("~/docker/toby"), Dockerfile: ptr("Dockerfile.toby")}, "/cfg", "/home")
	if err != nil {
		t.Fatal(err)
	}
	if build.Context != "/home/docker/toby" {
		t.Fatalf("context = %q", build.Context)
	}
	if build.Dockerfile != "/home/docker/toby/Dockerfile.toby" {
		t.Fatalf("dockerfile = %q", build.Dockerfile)
	}
}

func TestResolveBuildRejectsExplicitEmpty(t *testing.T) {
	if _, err := ResolveBuild(&Build{Context: ptr("  ")}, "/cfg", "/home"); err == nil {
		t.Fatal("expected empty context to fail")
	}
	if _, err := ResolveBuild(&Build{Dockerfile: ptr("")}, "/cfg", "/home"); err == nil {
		t.Fatal("expected empty dockerfile to fail")
	}
}
