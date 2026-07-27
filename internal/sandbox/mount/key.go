// Package mount defines native host-directory and bind-mount contracts shared
// by tools and the Bubblewrap sandbox runtime.
package mount

import (
	"fmt"
	"strings"
	"unicode"
)

// TypeTool identifies a tool-owned persistent volume.
const TypeTool = "tool"

const temporaryPublicationPrefix = ".toby-tmp-"

// Key identifies one Toby-managed persistent directory.
type Key struct {
	Type    string
	Name    string
	Purpose string
}

var _ fmt.Stringer = Key{}

// String returns the canonical textual representation.
func (k Key) String() string {
	return k.Type + "." + k.Name + "." + k.Purpose
}

// Validate verifies that every key component is safe for storage naming.
func (k Key) Validate() error {
	if err := validateComponent(k.Type); err != nil {
		return fmt.Errorf("mount key type: %w", err)
	}
	if k.Type != TypeTool {
		return fmt.Errorf("mount key type must be %q", TypeTool)
	}
	if err := validateComponent(k.Name); err != nil {
		return fmt.Errorf("mount key name: %w", err)
	}
	if err := validateComponent(k.Purpose); err != nil {
		return fmt.Errorf("mount key purpose: %w", err)
	}
	return nil
}

func validateComponent(value string) error {
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("must not have surrounding whitespace")
	}
	if value == "." || value == ".." {
		return fmt.Errorf("must not be %q", value)
	}
	if strings.HasPrefix(value, temporaryPublicationPrefix) {
		return fmt.Errorf("must not use Toby's reserved temporary-publication prefix")
	}
	if len(value) > 128 {
		return fmt.Errorf("must not exceed 128 bytes")
	}
	for _, r := range value {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			continue
		}
		return fmt.Errorf("contains invalid character %q", r)
	}
	return nil
}
