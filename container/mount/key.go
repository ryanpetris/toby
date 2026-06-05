package mount

// Mount keys and volume naming. A Key (type/name/purpose) uniquely identifies a
// managed mount; Volume derives the stable Docker volume name from a profile and
// Key as toby.<profile>.<type>.<name>.<purpose>.

import (
	"fmt"
	"strings"
)

const (
	TypeRuntime    = "runtime"
	TypeTool       = "tool"
	NameHome       = "home"
	PurposeDefault = "default"
)

// Key uniquely identifies a managed mount.
type Key struct {
	Type    string
	Name    string
	Purpose string
}

var _ fmt.Stringer = Key{}

func (k Key) String() string {
	if k.Purpose == "" {
		return k.Type + "." + k.Name
	}
	return k.Type + "." + k.Name + "." + k.Purpose
}

// RuntimeHomeKey is the key for the per-sandbox home volume.
func RuntimeHomeKey(sandboxName string) Key {
	purpose := strings.TrimSpace(sandboxName)
	if purpose == "" {
		purpose = PurposeDefault
	}
	return Key{Type: TypeRuntime, Name: NameHome, Purpose: purpose}
}

// IsRuntimeHome reports whether key is the runtime home volume.
func IsRuntimeHome(key Key) bool {
	return key.Type == TypeRuntime && key.Name == NameHome
}

// ParseKey parses "type.name" or "type.name.purpose".
func ParseKey(value string) (Key, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Key{}, fmt.Errorf("mount key must be type.name or type.name.purpose")
	}

	key := Key{Type: strings.TrimSpace(parts[0]), Name: strings.TrimSpace(parts[1])}
	if len(parts) == 3 {
		key.Purpose = strings.TrimSpace(parts[2])
	}
	if key.Type == "" || key.Name == "" || strings.ContainsAny(key.Type+key.Name+key.Purpose, "\x00") {
		return Key{}, fmt.Errorf("invalid mount key %q", value)
	}
	return key, nil
}

func validateKey(key Key) error {
	key.Type = strings.TrimSpace(key.Type)
	key.Name = strings.TrimSpace(key.Name)
	key.Purpose = strings.TrimSpace(key.Purpose)
	if key.Type == "" || key.Name == "" || key.Purpose == "" {
		return fmt.Errorf("mount key type, name, and purpose are required")
	}
	if strings.ContainsAny(key.Type+key.Name+key.Purpose, "\x00") {
		return fmt.Errorf("mount key contains invalid NUL byte")
	}

	return nil
}

// Volume returns the stable volume name for a profile and key:
// toby.<profile>.<type>.<name>.<purpose>.
func Volume(profile string, key Key) string {
	return strings.Join([]string{
		"toby",
		namePart(profile, "default"),
		namePart(key.Type, "mount"),
		namePart(key.Name, "default"),
		namePart(key.Purpose, "default"),
	}, ".")
}

func namePart(value, fallback string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if isNameChar(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), "-.")
	if name == "" {
		return fallback
	}
	return name
}

func isNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
}
