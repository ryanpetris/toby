package mount

// Shared-home volume naming. HomeVolume derives the stable Docker volume name for a
// profile's shared home as toby.<profile>.runtime.home.

import "strings"

const (
	TypeRuntime = "runtime"
	NameHome    = "home"
	// PurposeDefault is the fallback profile name when none is configured.
	PurposeDefault = "default"
)

// HomeVolume returns the stable shared-home volume name for a profile:
// toby.<profile>.runtime.home.
func HomeVolume(profile string) string {
	return strings.Join([]string{
		"toby",
		namePart(profile, PurposeDefault),
		TypeRuntime,
		NameHome,
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
