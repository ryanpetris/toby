package engine

import (
	"testing"
)

func TestNewServiceHasEmptySnapshot(t *testing.T) {
	s := New()
	if got := s.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %#v", got)
	}

	s.Forget(nil) // must not panic on a nil container
}

func TestShortID(t *testing.T) {
	if got := shortID("0123456789abcdef0123"); got != "0123456789ab" {
		t.Fatalf("shortID = %q", got)
	}

	if got := shortID("abc"); got != "abc" {
		t.Fatalf("shortID short = %q", got)
	}
}
