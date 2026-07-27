package uuid

// Covers UUID version, variant, formatting, and practical uniqueness.

import (
	"regexp"
	"testing"
)

var v4Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

var v8Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestNewV4(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 128)
	for range 128 {
		value, err := NewV4()
		if err != nil {
			t.Fatal(err)
		}
		if !v4Pattern.MatchString(value) {
			t.Fatalf("NewV4() = %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("NewV4() repeated %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestNewV8IsDeterministicAndPreservesCustomBits(t *testing.T) {
	t.Parallel()

	input := [16]byte{
		0x00, 0x11, 0x22, 0x33,
		0x44, 0x55, 0x66, 0x77,
		0xff, 0x99, 0xaa, 0xbb,
		0xcc, 0xdd, 0xee, 0xff,
	}
	first := NewV8(input)
	second := NewV8(input)

	if first != second {
		t.Fatalf("NewV8() produced %q and %q", first, second)
	}
	if !v8Pattern.MatchString(first) {
		t.Fatalf("NewV8() = %q", first)
	}
	if first != "00112233-4455-8677-bf99-aabbccddeeff" {
		t.Fatalf("NewV8() = %q", first)
	}
}
