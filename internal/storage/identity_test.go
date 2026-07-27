package storage

// Tests stable home-volume identity validation and hashing.

import (
	"strings"
	"testing"
)

func TestResolveHomeIdentityIsStableAndCollisionResistant(t *testing.T) {
	first, err := ResolveHomeIdentity("Personal Work", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveHomeIdentity("Personal Work", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("identities differ: %#v and %#v", first, second)
	}
	if len(first.ID) != 128 {
		t.Fatalf("unexpected ID %q", first.ID)
	}

	caseVariant, err := ResolveHomeIdentity("personal work", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == caseVariant.ID {
		t.Fatal("case-distinct names produced the same ID")
	}

	normalizationVariant, err := ResolveHomeIdentity(
		"Personal Wo\u0308rk",
		defaultProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == normalizationVariant.ID {
		t.Fatal("distinct Unicode byte strings produced the same ID")
	}

	profileVariant, err := ResolveHomeIdentity("Personal Work", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == profileVariant.ID {
		t.Fatal("different home profiles produced the same ID")
	}
	if first.Profile != defaultProfile ||
		profileVariant.Profile != "personal" {
		t.Fatalf(
			"identity profiles = default:%q variant:%q",
			first.Profile,
			profileVariant.Profile,
		)
	}
}

func TestResolveHomeIdentityKeepsUntrustedTextOutOfPath(t *testing.T) {
	identity, err := ResolveHomeIdentity(
		"../../✨/Workspace",
		defaultProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(identity.ID, `/\`) || strings.Contains(identity.ID, "..") {
		t.Fatalf("unsafe identity path component %q", identity.ID)
	}

	fallback, err := ResolveHomeIdentity("✨", defaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.ID) != 128 {
		t.Fatalf("unicode ID = %q", fallback.ID)
	}
}

func TestResolveHomeIdentityRejectsInvalidNames(t *testing.T) {
	tests := []string{
		"",
		string([]byte{0xff}),
		strings.Repeat("x", maxHomeNameBytes+1),
	}
	for _, value := range tests {
		if _, err := ResolveHomeIdentity(value, defaultProfile); err == nil {
			t.Fatalf("accepted invalid name %q", value)
		}
	}

	if _, err := ResolveHomeIdentity("home", ""); err == nil {
		t.Fatal("accepted an empty home profile")
	}
}
