package storage

// Resolves optional per-tool overrides from the launch-wide storage profile.

import "strings"

const defaultProfile = "default"

// ProfileSelection is the effective tool-volume profile configuration for one
// launch after its home profile has been selected.
type ProfileSelection struct {
	Default string
	Tools   map[string]string
}

// ProfileFor returns the normalized profile selected for a tool.
func (s ProfileSelection) ProfileFor(tool string) string {
	if profile := strings.TrimSpace(s.Tools[tool]); profile != "" {
		return profile
	}
	if profile := strings.TrimSpace(s.Default); profile != "" {
		return profile
	}
	return defaultProfile
}
