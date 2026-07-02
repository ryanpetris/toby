package mount

import (
	"testing"

	"petris.dev/toby/container/layout"
)

func TestHomeVolumeNaming(t *testing.T) {
	if got := HomeVolume("default"); got != "toby.default.runtime.home" {
		t.Fatalf("home volume = %q", got)
	}
	if got := HomeVolume("work profile"); got != "toby.work-profile.runtime.home" {
		t.Fatalf("sanitized home volume = %q", got)
	}
	if got := HomeVolume(""); got != "toby.default.runtime.home" {
		t.Fatalf("empty profile home volume = %q", got)
	}
}

func TestConfigureHomePointsAtProfile(t *testing.T) {
	s := New()
	if got := s.Profile(); got != PurposeDefault {
		t.Fatalf("unconfigured profile = %q", got)
	}
	s.ConfigureHome("work")
	if s.Profile() != "work" || s.HomeVolume() != "toby.work.runtime.home" {
		t.Fatalf("profile = %q volume = %q", s.Profile(), s.HomeVolume())
	}
}

func TestConfigureHomeResetsBinds(t *testing.T) {
	s := New()
	s.ConfigureHome("default")
	if err := s.AddBind(Bind{HostPath: "/x", Target: "~/x"}); err != nil {
		t.Fatal(err)
	}
	s.ConfigureHome("default")
	if len(s.Binds()) != 0 {
		t.Fatalf("binds not reset: %#v", s.Binds())
	}
}

func TestAddBindExpandsAndDedups(t *testing.T) {
	s := New()
	s.ConfigureHome("default")

	bind := Bind{HostPath: "/var/run/docker.sock", Target: "/var/run/docker.sock", Access: AccessDev}
	if err := s.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBind(Bind{HostPath: "/host/.docker", Target: "~/.docker", Access: AccessReadOnly}); err != nil {
		t.Fatal(err)
	}

	binds := s.Binds()
	if len(binds) != 2 {
		t.Fatalf("binds = %#v", binds)
	}
	if binds[1].Target != layout.Home+"/.docker" {
		t.Fatalf("bind target not expanded: %#v", binds[1])
	}
}

func TestAddBindRejectsEmptyHostPath(t *testing.T) {
	s := New()
	s.ConfigureHome("default")
	if err := s.AddBind(Bind{Target: "~/x"}); err == nil {
		t.Fatal("expected empty host path to be rejected")
	}
}
