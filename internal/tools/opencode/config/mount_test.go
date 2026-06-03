package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control/httpproxy"
	sandboxpath "petris.dev/toby/internal/sandbox/path"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

var testInstructions = []string{"/run/user/1000/toby/context/user-instructions.md"}
var testControlHost = "127.0.0.1:12345"
var testTobyMCPURL = "http://127.0.0.1:12345/proxy/toby"

const testHome = "/toby/home"

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
}

func TestGeneratedConfigIncludesDefaultPermissionPaths(t *testing.T) {
	projectRoot := "/toby/workspace"
	config := readGeneratedConfig(t, &http.Client{}, projectRoot, testInstructions)

	external := config["permission"].(map[string]any)["external_directory"].(map[string]any)
	for _, pattern := range []string{
		projectRoot, projectRoot + "/**",
		"/tmp", "/tmp/**",
		testHome + "/go", testHome + "/go/**",
		testHome + "/.cache", testHome + "/.cache/**",
	} {
		if external[pattern] != "allow" {
			t.Fatalf("external_directory[%q] = %#v, want allow", pattern, external[pattern])
		}
	}
}

func TestGeneratedConfigPermissionUserOverride(t *testing.T) {
	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"permissions": map[string]any{
			"paths": map[string]any{
				"/tmp":    "deny",
				"/custom": "allow",
			},
		},
	})
	cfg, err := tobyconfig.Load(cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := readGeneratedConfigWithTobyConfig(t, &http.Client{}, "/toby/workspace", testInstructions, cfg)

	external := config["permission"].(map[string]any)["external_directory"].(map[string]any)
	if external["/tmp"] != "deny" {
		t.Fatalf("user override external_directory[/tmp] = %#v, want deny", external["/tmp"])
	}
	if external["/custom"] != "allow" {
		t.Fatalf("external_directory[/custom] = %#v, want allow", external["/custom"])
	}
	if external["/toby/workspace"] != "allow" {
		t.Fatalf("default external_directory[/toby/workspace] = %#v, want allow", external["/toby/workspace"])
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
		"providers": map[string]any{
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

func TestGeneratedConfigIncludesFetchedAnthropicModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude","display_name":"Claude"},{"id":"fallback"}]}`))
	}))
	t.Cleanup(server.Close)

	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"providers": map[string]any{
			"anthropic": map[string]any{
				"type":    "anthropic",
				"baseURL": server.URL,
			},
		},
	})
	cfg, err := tobyconfig.Load(cfgDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := readGeneratedConfigWithTobyConfig(t, server.Client(), filepath.Join(t.TempDir(), "Projects"), testInstructions, cfg)

	provider := config["provider"].(map[string]any)["anthropic"].(map[string]any)
	if provider["npm"] != "@ai-sdk/anthropic" {
		t.Fatalf("provider npm = %#v", provider["npm"])
	}
	models := provider["models"].(map[string]any)
	if name := models["claude"].(map[string]any)["name"]; name != "Claude" {
		t.Fatalf("claude model name = %#v, want Claude", name)
	}
	if name := models["fallback"].(map[string]any)["name"]; name != "fallback" {
		t.Fatalf("fallback model name = %#v, want fallback", name)
	}
}

func TestGeneratedConfigUsesConfiguredProviderModelsVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "models should not be fetched", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	cfgDir := t.TempDir()
	writeJSON(t, filepath.Join(cfgDir, "config.json"), map[string]any{
		"mcps": map[string]any{
			"docs": map[string]any{"type": "remote", "url": "https://example.com/mcp"},
		},
		"providers": map[string]any{
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
		"providers": map[string]any{
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

func contextFilesWithTobyConfig(t *testing.T, client *http.Client, projectRoot string, instructions []string, cfg *tobyconfig.Service) ([]contextfiles.File, []error, error) {
	t.Helper()
	renderer, service, proxy := testDeps(t, client)
	builder := service.NewBuilder()
	paths := sandboxpath.Paths{Home: testHome, Workspace: projectRoot}
	warnings, err := renderer.RegisterContextFiles(context.Background(), builder, paths, testControlHost, testTobyMCPURL, instructions, cfg, proxy, nil)
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
