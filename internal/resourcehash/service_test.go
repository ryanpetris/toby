package resourcehash

// Covers stable serialization, typed distinctions, and protected-value input.

import (
	"strings"
	"testing"
)

func TestSumIsStableAcrossMapInsertionOrder(t *testing.T) {
	t.Parallel()

	service := NewService()
	first, err := service.Sum(map[string]any{
		"schema": 1,
		"configuration": map[string]any{
			"url":     "https://example.invalid",
			"headers": map[string]string{"B": "two", "A": "one"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Sum(map[string]any{
		"configuration": map[string]any{
			"headers": map[string]string{"A": "one", "B": "two"},
			"url":     "https://example.invalid",
		},
		"schema": 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatalf("equivalent identities differ:\n%s\n%s", first, second)
	}
	if !strings.HasPrefix(first.String(), Algorithm+":") {
		t.Fatalf("identity %q lacks algorithm prefix", first)
	}
	if len(first.String()) != len(Algorithm)+1+128 {
		t.Fatalf("identity length = %d", len(first.String()))
	}
	if !strings.HasPrefix(first.UUID()[14:], "8") {
		t.Fatalf("resource UUID %q is not version 8", first.UUID())
	}
	if first.UUID()[19] != '8' &&
		first.UUID()[19] != '9' &&
		first.UUID()[19] != 'a' &&
		first.UUID()[19] != 'b' {
		t.Fatalf("resource UUID %q has an invalid variant", first.UUID())
	}
	if first.UUID() != second.UUID() {
		t.Fatalf(
			"equivalent identities produced UUIDs %q and %q",
			first.UUID(),
			second.UUID(),
		)
	}
}

func TestSumIncludesKindAndProtectedValues(t *testing.T) {
	t.Parallel()

	service := NewService()
	base := map[string]any{
		"schema":        1,
		"kind":          "resource.mcp",
		"configuration": map[string]any{"token": "first"},
	}
	first, err := service.Sum(base)
	if err != nil {
		t.Fatal(err)
	}

	base["kind"] = "resource.models"
	otherKind, err := service.Sum(base)
	if err != nil {
		t.Fatal(err)
	}
	if first == otherKind {
		t.Fatal("different resource kinds have the same identity")
	}

	base["kind"] = "resource.mcp"
	base["configuration"] = map[string]any{"token": "second"}
	otherSecret, err := service.Sum(base)
	if err != nil {
		t.Fatal(err)
	}
	if first == otherSecret {
		t.Fatal("different protected values have the same identity")
	}
}

func TestSumRejectsUnsupportedValue(t *testing.T) {
	t.Parallel()

	if _, err := NewService().Sum(make(chan struct{})); err == nil {
		t.Fatal("Sum() accepted a channel")
	}
}
