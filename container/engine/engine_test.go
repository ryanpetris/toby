package engine

import (
	"runtime"
	"testing"
)

func TestNewServiceHasEmptySnapshot(t *testing.T) {
	s := New()
	if got := s.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %#v", got)
	}

	s.Forget(nil) // must not panic on a nil container
}

func TestClassifyDaemon(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("desktop platforms always classify as DaemonDesktop")
	}

	cases := []struct {
		host string
		want DaemonClass
	}{
		{"unix:///var/run/docker.sock", DaemonLocalUnix},
		{"", DaemonLocalUnix},
		{"tcp://10.0.0.1:2375", DaemonRemote},
		{"ssh://user@host", DaemonRemote},
	}
	for _, c := range cases {
		if got := classifyDaemon(c.host); got != c.want {
			t.Fatalf("classifyDaemon(%q) = %d, want %d", c.host, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("0123456789abcdef0123"); got != "0123456789ab" {
		t.Fatalf("shortID = %q", got)
	}

	if got := shortID("abc"); got != "abc" {
		t.Fatalf("shortID short = %q", got)
	}
}
