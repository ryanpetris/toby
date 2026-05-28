package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/config"
)

func TestOpenCodeSyncModelsRewritesModelsToExactIDs(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen/qwen3-coder:30b-a3b-instruct"},{"id":"gpt-5.1"},{"id":"gpt-5.1"}]}`))
	}))
	defer server.Close()

	root := t.TempDir()
	configDir := filepath.Join(root, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "token"), []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "opencode.json")
	input := `{
  "provider": {
    "local": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "` + server.URL + `",
        "apiKey": "{file:token}"
      },
      "models": {
        "old": {"name": "Old", "custom": true}
      }
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &openCodeTool{paths: config.Paths{SandboxRoot: root, Home: t.TempDir()}, http: server.Client()}
	if err := tool.syncModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	provider := parsed["provider"].(map[string]any)["local"].(map[string]any)
	models := provider["models"].(map[string]any)
	if len(models) != 2 {
		t.Fatalf("models length = %d, want 2: %#v", len(models), models)
	}
	for _, id := range []string{"qwen/qwen3-coder:30b-a3b-instruct", "gpt-5.1"} {
		model := models[id].(map[string]any)
		if len(model) != 1 || model["name"] != id {
			t.Fatalf("model %s = %#v", id, model)
		}
	}
}
