package storage

// Derives private-home volume identities from their canonical metadata.

import (
	"fmt"
	"unicode/utf8"
)

const maxHomeNameBytes = 1024

// HomeIdentity pairs the exact display name and profile with their stable
// directory ID.
type HomeIdentity struct {
	ID          string
	DisplayName string
	Profile     string
}

// ResolveHomeIdentity validates the name and profile and derives their
// metadata hash.
func ResolveHomeIdentity(name, profile string) (HomeIdentity, error) {
	if name == "" {
		return HomeIdentity{}, fmt.Errorf("private-home name must not be empty")
	}
	if len(name) > maxHomeNameBytes {
		return HomeIdentity{}, fmt.Errorf("private-home name exceeds %d bytes", maxHomeNameBytes)
	}
	if !utf8.ValidString(name) {
		return HomeIdentity{}, fmt.Errorf("private-home name is not valid UTF-8")
	}

	id, _, err := volumeID(
		newHomeMetadata(profile, name),
		DefaultLimits().MetadataSize,
	)
	if err != nil {
		return HomeIdentity{}, fmt.Errorf("derive private-home volume identity: %w", err)
	}

	return HomeIdentity{
		ID:          id,
		DisplayName: name,
		Profile:     profile,
	}, nil
}
