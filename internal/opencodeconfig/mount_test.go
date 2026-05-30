package opencodeconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/contextfiles"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/tobyconfig"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

var testInstructions = []string{"/run/user/1000/toby/context/GIT_AGENTS.md"}
var testControlHost = "127.0.0.1:12345"
var testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

func TestNewRendererRequiresHTTPClient(t *testing.T) {
	if _, err := NewRenderer(nil); err == nil {
		t.Fatal("expected nil HTTP client to fail")
	}
}

func TestGeneratedConfigIncludesTobySettings(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "Projects")
	config := readGeneratedConfig(t, &http.Client{}, projectRoot, testInstructions)

	mcp := config["mcp"].(map[string]any)
	toby := mcp["toby"].(map[string]any)
	if toby["type"] != "remote" || toby["url"] != testTobyMCPURL || toby["enabled"] != true {
		t.Fatalf("mcp.toby = %#v", toby)
	}

	instructions := config["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != testInstructions[0] {
		t.Fatalf("instructions = %#v", instructions)
	}

	external := config["permission"].(map[string]any)["external_directory"].(map[string]any)
	for _, pattern := range []string{"/tmp", "/tmp/**", projectRoot, filepath.Join(projectRoot, "**")} {
		if external[pattern] != "allow" {
			t.Fatalf("external_directory[%q] = %#v, want allow", pattern, external[pattern])
		}
	}
}

func TestGeneratedConfigIncludesFetchedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
	}))
	t.Cleanup(server.Close)

	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"provider": map[string]any{
			"local": map[string]any{
				"type":    "openai",
				"baseURL": server.URL,
			},
		},
	})
	cfg, err := tobyconfig.Load(cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := readGeneratedConfigWithTobyConfig(t, server.Client(), filepath.Join(t.TempDir(), "Projects"), testInstructions, cfg)

	provider := config["provider"].(map[string]any)["local"].(map[string]any)
	options := provider["options"].(map[string]any)
	if baseURL, _ := options["baseURL"].(string); !strings.HasPrefix(baseURL, "http://"+testControlHost+"/proxy/") {
		t.Fatalf("provider options = %#v", options)
	}
	models := provider["models"].(map[string]any)
	if _, ok := models["alpha"]; !ok {
		t.Fatalf("models = %#v, want alpha", models)
	}
	if _, ok := models["beta"]; !ok {
		t.Fatalf("models = %#v, want beta", models)
	}
}

func TestGeneratedConfigUsesConfiguredProviderModelsVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "models should not be fetched", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"mcp": map[string]any{
			"docs": map[string]any{"type": "remote", "url": "https://example.com/mcp"},
		},
		"provider": map[string]any{
			"local": map[string]any{
				"type":    "openai",
				"baseURL": server.URL,
				"models": map[string]any{
					"custom": map[string]any{"name": "Configured Custom"},
				},
			},
		},
	})
	cfg, err := tobyconfig.Load(cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := readGeneratedConfigWithTobyConfig(t, server.Client(), filepath.Join(t.TempDir(), "Projects"), testInstructions, cfg)

	mcp := config["mcp"].(map[string]any)
	if docs := mcp["docs"].(map[string]any); docs["type"] != "remote" {
		t.Fatalf("mcp.docs = %#v", docs)
	} else {
		if url, _ := docs["url"].(string); !strings.HasPrefix(url, "http://"+testControlHost+"/proxy/") {
			t.Fatalf("mcp.docs.url = %#v", docs["url"])
		}
	}
	models := config["provider"].(map[string]any)["local"].(map[string]any)["models"].(map[string]any)
	custom := models["custom"].(map[string]any)
	if custom["name"] != "Configured Custom" {
		t.Fatalf("models = %#v", models)
	}
}

func TestGeneratedConfigReturnsModelFetchWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"provider": map[string]any{
			"local": map[string]any{
				"type":    "openai",
				"baseURL": server.URL,
			},
		},
	})
	cfg, err := tobyconfig.Load(cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, warnings, err := contextFilesWithTobyConfig(t, server.Client(), filepath.Join(t.TempDir(), "Projects"), testInstructions, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", warnings)
	}
	file := findStaticFile(files, StaticConfigPath)
	if file == nil {
		t.Fatalf("files = %#v, want %s", files, StaticConfigPath)
	}
	var config map[string]any
	if err := json.Unmarshal(file.Data, &config); err != nil {
		t.Fatal(err)
	}
	if provider, ok := config["provider"].(map[string]any); ok {
		if _, exists := provider["local"]; exists {
			t.Fatalf("failed provider was not excluded: %#v", provider)
		}
	}
}

func TestGeneratedFilesAreReadOnly(t *testing.T) {
	files, warnings, err := contextFiles(t, &http.Client{}, filepath.Join(t.TempDir(), "Projects"), testInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, path := range []string{StaticGitignorePath, StaticConfigPath} {
		file := findStaticFile(files, path)
		if file == nil {
			t.Fatalf("files = %#v, want %s", files, path)
		}
		if file.Mode != 0o400 {
			t.Fatalf("mode %s = %#o, want 0400", path, file.Mode)
		}
	}
	if file := findStaticFile(files, StaticGitignorePath); string(file.Data) != "*\n" {
		t.Fatalf("gitignore = %q, want *", file.Data)
	}
}

func readGeneratedConfig(t *testing.T, client *http.Client, projectRoot string, instructions []string) map[string]any {
	return readGeneratedConfigWithTobyConfig(t, client, projectRoot, instructions, nil)
}

func readGeneratedConfigWithTobyConfig(t *testing.T, client *http.Client, projectRoot string, instructions []string, cfg *tobyconfig.Service) map[string]any {
	t.Helper()
	files, warnings, err := contextFilesWithTobyConfig(t, client, projectRoot, instructions, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	file := findStaticFile(files, StaticConfigPath)
	if file == nil {
		t.Fatalf("files = %#v, want %s", files, StaticConfigPath)
	}
	var config map[string]any
	if err := json.Unmarshal(file.Data, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func contextFiles(t *testing.T, client *http.Client, projectRoot string, instructions []string) ([]contextfiles.File, []error, error) {
	return contextFilesWithTobyConfig(t, client, projectRoot, instructions, nil)
}

func contextFilesWithTobyConfig(t *testing.T, client *http.Client, projectRoot string, instructions []string, cfg *tobyconfig.Service) ([]contextfiles.File, []error, error) {
	t.Helper()
	renderer, service, proxy := testDeps(t, client)
	builder := service.NewBuilder()
	warnings, err := renderer.RegisterContextFiles(context.Background(), builder, projectRoot, testControlHost, testTobyMCPURL, instructions, cfg, proxy)
	if err != nil {
		return nil, warnings, err
	}
	return builder.Files(), warnings, nil
}

func testDeps(t *testing.T, client *http.Client) (*Renderer, *contextfiles.Service, *httpproxy.Service) {
	t.Helper()
	var renderer *Renderer
	var service *contextfiles.Service
	var proxy *httpproxy.Service
	app := fxtest.New(t,
		fx.Supply(client),
		fx.Provide(NewRenderer, contextfiles.NewService, httpproxy.NewService),
		fx.Populate(&renderer, &service, &proxy),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	return renderer, service, proxy
}

func writeJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findStaticFile(files []contextfiles.File, path string) *contextfiles.File {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}
