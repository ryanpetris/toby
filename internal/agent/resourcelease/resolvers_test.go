package resourcelease

// Verifies resource-specific defaulting precedes stable identity hashing.

import (
	"encoding/json"
	"regexp"
	"runtime"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/resourcehash"
)

var resourceIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-8[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestOCIResolverDefaultsProduceStableEffectiveIdentity(t *testing.T) {
	resolver, err := NewOCIResolver(resourcehash.NewService())
	if err != nil {
		t.Fatalf("new OCI resolver: %v", err)
	}

	implicit, err := resolver.Resolve(
		t.Context(),
		json.RawMessage(`{"reference":"example.invalid/root:latest","platform":{}}`),
	)
	if err != nil {
		t.Fatalf("resolve implicit defaults: %v", err)
	}
	explicit, err := resolver.Resolve(
		t.Context(),
		json.RawMessage(
			`{"reference":"example.invalid/root:latest","platform":{"architecture":"`+
				runtime.GOARCH+
				`","os":"linux"},"pull_policy":"`+
				string(image.PullIfMissing)+
				`"}`,
		),
	)
	if err != nil {
		t.Fatalf("resolve explicit defaults: %v", err)
	}
	if implicit.ID != explicit.ID {
		t.Fatalf(
			"implicit ID %q differs from explicit ID %q",
			implicit.ID,
			explicit.ID,
		)
	}
	if implicit.Kind != protocol.ResourceOCI {
		t.Fatalf("resolved kind = %q", implicit.Kind)
	}
	if !resourceIDPattern.MatchString(string(implicit.ID)) {
		t.Fatalf("resolved resource ID %q is not UUIDv8", implicit.ID)
	}
	if implicit.Digest.IsZero() {
		t.Fatal("resolved resource digest is empty")
	}
}

func TestTypedResolverRejectsUnknownConfigurationField(t *testing.T) {
	resolver, err := NewModelsResolver(resourcehash.NewService())
	if err != nil {
		t.Fatalf("new models resolver: %v", err)
	}

	_, err = resolver.Resolve(
		t.Context(),
		json.RawMessage(
			`{"protocol":"openai","url":"https://example.invalid","unknown":true}`,
		),
	)
	if err == nil {
		t.Fatal("resolver accepted an unknown field")
	}
}
