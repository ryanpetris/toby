package providers

import (
	"context"
	"testing"
)

type countingClient struct {
	kind   Kind
	models []Model
	calls  int
}

func (c *countingClient) Kind() Kind { return c.kind }

func (c *countingClient) LookupModels(_ context.Context, _ string, _ map[string]string) ([]Model, error) {
	c.calls++
	return c.models, nil
}

func TestRegistryCachesSuccessfulLookups(t *testing.T) {
	client := &countingClient{kind: KindOpenAI, models: []Model{{ID: "a", DisplayName: "a"}}}
	registry := NewRegistry([]Client{client})

	for i := 0; i < 3; i++ {
		models, err := registry.LookupModels(context.Background(), KindOpenAI, "https://example.test", map[string]string{"X": "1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 || models[0].ID != "a" {
			t.Fatalf("models = %#v", models)
		}
	}

	if client.calls != 1 {
		t.Fatalf("client.calls = %d, want 1 (result should be cached)", client.calls)
	}
}

func TestRegistryCacheKeyedByArguments(t *testing.T) {
	client := &countingClient{kind: KindOpenAI, models: []Model{{ID: "a", DisplayName: "a"}}}
	registry := NewRegistry([]Client{client})

	if _, err := registry.LookupModels(context.Background(), KindOpenAI, "https://one.test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.LookupModels(context.Background(), KindOpenAI, "https://two.test", nil); err != nil {
		t.Fatal(err)
	}

	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want 2 (different baseURL must not share cache)", client.calls)
	}
}

func TestRegistryUnknownKind(t *testing.T) {
	registry := NewRegistry(nil)
	if _, err := registry.LookupModels(context.Background(), KindAnthropic, "https://example.test", nil); err == nil {
		t.Fatal("expected error for unregistered kind")
	}
}
