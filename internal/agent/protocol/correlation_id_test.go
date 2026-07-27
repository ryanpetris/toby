package protocol

// Verifies client-generated correlation UUID format and practical uniqueness.

import (
	"regexp"
	"testing"
)

func TestNewCorrelationIDUsesUUIDVersionFour(t *testing.T) {
	pattern := regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	seen := make(map[CorrelationID]struct{}, 128)

	for range 128 {
		id, err := NewCorrelationID()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(string(id)) {
			t.Fatalf("NewCorrelationID() = %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("NewCorrelationID() repeated %q", id)
		}
		seen[id] = struct{}{}
	}
}
