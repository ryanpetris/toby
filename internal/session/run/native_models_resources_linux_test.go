//go:build linux

package run

// Exercises conversion of agent-discovered model metadata into tool-facing
// models endpoint configuration and session introspection.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/tobymcp"
)

func TestNativeModelsDocumentPreservesDiscoveredMetadata(t *testing.T) {
	document, err := nativeModelsDocument([]protocol.ModelsListItemResponse{
		{
			ModelID: "alpha",
			Model: json.RawMessage(
				`{"name":"Alpha","limit":9007199254740993}`,
			),
		},
		{
			ModelID: "beta",
			Model:   json.RawMessage(`{"name":"Beta"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	alpha, ok := document["alpha"].(map[string]any)
	if !ok || alpha["name"] != "Alpha" {
		t.Fatalf("alpha metadata = %#v", document["alpha"])
	}
	if got := alpha["limit"]; got != json.Number("9007199254740993") {
		t.Fatalf("alpha limit = %#v", got)
	}
	if beta, ok := document["beta"].(map[string]any); !ok ||
		beta["name"] != "Beta" {
		t.Fatalf("beta metadata = %#v", document["beta"])
	}
}

func TestNativeModelsDocumentRejectsDuplicateIDs(t *testing.T) {
	_, err := nativeModelsDocument([]protocol.ModelsListItemResponse{
		{ModelID: "alpha", Model: json.RawMessage(`{}`)},
		{ModelID: "alpha", Model: json.RawMessage(`{}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "appears more than once") {
		t.Fatalf("error = %v, want duplicate model ID", err)
	}
}

func TestNativeSnapshotModelsUsesDiscoveredModels(t *testing.T) {
	got := nativeSnapshotModels([]sessionconfig.ModelsEndpoint{
		{
			ID:     "zeta",
			Type:   "anthropic",
			Models: map[string]any{"z-two": nil, "z-one": nil},
		},
		{
			ID:     "alpha",
			Type:   "openai",
			Models: map[string]any{"a-one": nil},
		},
	})
	want := []struct {
		name   string
		kind   string
		models []string
	}{
		{name: "alpha", kind: "openai", models: []string{"a-one"}},
		{
			name:   "zeta",
			kind:   "anthropic",
			models: []string{"z-one", "z-two"},
		},
	}
	if len(got) != len(want) {
		t.Fatalf("models endpoints = %#v", got)
	}
	for index, expected := range want {
		if got[index].Name != expected.name ||
			got[index].Type != expected.kind ||
			!reflect.DeepEqual(got[index].Models, expected.models) {
			t.Fatalf(
				"models endpoint %d = %#v, want %#v",
				index,
				got[index],
				expected,
			)
		}
	}
}

func TestNativeSnapshotModelsBoundsDiscoveredModels(t *testing.T) {
	models := make(map[string]any, 300)
	for index := range 300 {
		models[fmt.Sprintf("model-%03d", index)] = nil
	}

	got := nativeSnapshotModels([]sessionconfig.ModelsEndpoint{{
		ID:     "local",
		Type:   "openai",
		Models: models,
	}})
	if len(got) != 1 ||
		len(got[0].Models) != tobymcp.MaxSnapshotCollectionItems {
		t.Fatalf("models endpoints = %#v", got)
	}
	if got[0].Models[0] != "model-000" ||
		got[0].Models[len(got[0].Models)-1] != "model-255" {
		t.Fatalf("bounded models = %#v", got[0].Models)
	}
}
