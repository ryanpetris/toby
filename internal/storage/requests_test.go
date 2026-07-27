package storage

// Tests deterministic tool-volume defaults, coalescing, and conflict detection
// without touching persistent storage.

import (
	"errors"
	"reflect"
	"testing"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestNormalizeRequestsDefaultsOrdersAndCoalesces(t *testing.T) {
	first := mount.Request{
		Key:      mount.Key{Type: mount.TypeTool, Name: "codex", Purpose: "state"},
		Target:   "~/.codex/state",
		Optional: true,
	}
	second := mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "data"},
		Target: "~/.local/share/opencode",
	}

	got, err := normalizeRequests([]mount.Request{second, first, first}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []mount.Request{
		{
			Key:      first.Key,
			Target:   "/toby/home/.codex/state",
			Access:   mount.AccessRegular,
			Optional: true,
		},
		{
			Key:    second.Key,
			Target: "/toby/home/.local/share/opencode",
			Access: mount.AccessRegular,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeRequests = %#v, want %#v", got, want)
	}
}

func TestNormalizeRequestsRejectsConflicts(t *testing.T) {
	base := mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: "opencode", Purpose: "data"},
		Target: "~/.local/share/opencode",
	}

	t.Run("same key differs", func(t *testing.T) {
		other := base
		other.Access = mount.AccessReadOnly
		_, err := normalizeRequests([]mount.Request{base, other}, nil)
		if !errors.Is(err, ErrConflictingRequest) {
			t.Fatalf("error = %v, want ErrConflictingRequest", err)
		}
	})

	t.Run("nested target", func(t *testing.T) {
		other := mount.Request{
			Key:    mount.Key{Type: mount.TypeTool, Name: "nested", Purpose: "cache"},
			Target: "~/.local/share/opencode/cache",
		}
		if _, err := normalizeRequests([]mount.Request{base, other}, nil); err == nil {
			t.Fatal("nested target was accepted")
		}
	})

	t.Run("occupied target", func(t *testing.T) {
		if _, err := normalizeRequests([]mount.Request{base}, []string{"/toby/home/.local"}); err == nil {
			t.Fatal("occupied target overlap was accepted")
		}
	})

	t.Run("home-relative occupied target", func(t *testing.T) {
		if _, err := normalizeRequests([]mount.Request{base}, []string{"~/.local"}); err == nil {
			t.Fatal("home-relative occupied target overlap was accepted")
		}
	})

	for _, target := range []string{"relative", "/toby/home/../escape", "/toby/home/\x00escape"} {
		t.Run("invalid occupied target "+target, func(t *testing.T) {
			if _, err := normalizeRequests([]mount.Request{base}, []string{target}); err == nil {
				t.Fatalf("invalid occupied target %q was accepted", target)
			}
		})
	}
}
