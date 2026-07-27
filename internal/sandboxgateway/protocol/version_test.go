package protocol

// Tests sandbox protocol version selection and mismatch reporting.

import (
	"errors"
	"testing"
)

func TestSupportedVersions(t *testing.T) {
	versions := SupportedVersions()
	if len(versions) != 1 || versions[0] != Version {
		t.Fatalf("supported versions = %v, want [%d]", versions, Version)
	}
	if !SupportsVersion(Version) {
		t.Fatalf("version %d is not supported", Version)
	}
	if SupportsVersion(Version + 1) {
		t.Fatalf("unexpected support for version %d", Version+1)
	}
}

func TestVersionErrorUnwrapsMismatch(t *testing.T) {
	err := VersionError{
		Received:  Version + 1,
		Supported: SupportedVersions(),
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error %v does not wrap %v", err, ErrVersionMismatch)
	}
}
