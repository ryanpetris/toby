// Package project is the daemon's registry of long-lived per-project containers. It
// owns the Starting/Ready/Draining/Gone lifecycle, session refcounting, and idle
// teardown; the heavy container bring-up plugs in behind the Lifecycle interface so
// the race-safe state machine is exercised on its own.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Key identifies a project container: the environment label plus the exact set of
// host project paths it was launched with. Same key => shared container; a different
// project set => a distinct container. The digest keys the registry map; the label is
// kept for status.
type Key struct {
	Label  string
	Digest string
}

// NewKey derives a Key from the environment label, the home profile, and the resolved
// project host paths. Paths are sorted so ordering does not change identity; the
// profile keys distinct netns containers so the same project on two profiles does not
// share one. The digest keys the registry map; the label is kept for status.
func NewKey(label, profile string, projectPaths []string) Key {
	paths := append([]string(nil), projectPaths...)
	sort.Strings(paths)

	h := sha256.New()
	h.Write([]byte(label))
	h.Write([]byte{0})
	h.Write([]byte(profile))
	h.Write([]byte{0})
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return Key{Label: label, Digest: hex.EncodeToString(h.Sum(nil))}
}

// String renders the key for logs: the label and a short digest prefix.
func (k Key) String() string {
	digest := k.Digest
	if len(digest) > 8 {
		digest = digest[:8]
	}
	return strings.TrimSpace(k.Label + "@" + digest)
}
