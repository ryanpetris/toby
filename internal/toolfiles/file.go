package toolfiles

// Defines one tool-owned native file and validates its registration fields.

import (
	"fmt"
	"io/fs"
	"strings"

	"petris.dev/toby/internal/sandbox/mount"
)

// File is one complete Toby-owned native file registered by a tool adapter.
type File struct {
	Owner  string
	Target string
	Data   []byte
	Mode   fs.FileMode
	UID    int
	GID    int
}

func (f File) clone() File {
	clone := f
	clone.Data = append([]byte(nil), f.Data...)
	return clone
}

func (f File) validate() error {
	if err := validateOwner(f.Owner); err != nil {
		return err
	}
	if err := mount.ValidateTarget(f.Target); err != nil {
		return fmt.Errorf("file owner %q target: %w", f.Owner, err)
	}
	if f.Mode.Perm() == 0 || f.Mode&^fs.ModePerm != 0 {
		return fmt.Errorf(
			"file owner %q mode must contain only nonzero permission bits: %v",
			f.Owner,
			f.Mode,
		)
	}
	if f.Mode.Perm()&0o600 == 0 {
		return fmt.Errorf(
			"file owner %q mode must grant the owner read or write permission: %v",
			f.Owner,
			f.Mode,
		)
	}
	if f.UID < 0 || f.GID < 0 {
		return fmt.Errorf(
			"file owner %q uid and gid must be non-negative",
			f.Owner,
		)
	}

	return nil
}

func validateOwner(owner string) error {
	if owner == "" || len(owner) > 128 || owner != strings.TrimSpace(owner) {
		return fmt.Errorf("file owner %q must be a nonempty tool name of at most 128 bytes", owner)
	}
	for index, character := range owner {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			index > 0 && (character == '.' || character == '_' || character == '-') {
			continue
		}
		return fmt.Errorf("file owner %q contains an invalid character", owner)
	}

	return nil
}
