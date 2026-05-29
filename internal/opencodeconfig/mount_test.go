package opencodeconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/staticfiles"
	"petris.dev/toby/internal/staticmount"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

var testInstructions = []string{"/home/petris/.local/state/toby/static/GIT_AGENTS.md"}
var testProjectInstructions = []string{"/home/petris/.local/state/toby/static/GIT_AGENTS.md", "/home/petris/.local/state/toby/static/PROJECT_MOUNT_AGENTS.md"}

func TestNewRendererRequiresHTTPClient(t *testing.T) {
	if _, err := NewRenderer(nil); err == nil {
		t.Fatal("expected nil HTTP client to fail")
	}
}

func TestGeneratedConfigIncludesTobySettings(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "Projects")
	config := readGeneratedConfig(t, &http.Client{}, t.TempDir(), projectRoot, testInstructions)

	mcp := config["mcp"].(map[string]any)
	toby := mcp["toby"].(map[string]any)
	if toby["type"] != "local" || toby["enabled"] != true {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	if got := toby["command"].([]any); len(got) != 2 || got[0] != "toby" || got[1] != "mcp" {
		t.Fatalf("mcp.toby.command = %#v", got)
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

	configDir := t.TempDir()
	writeJSON(t, filepath.Join(configDir, "opencode.json"), map[string]any{
		"provider": map[string]any{
			"local": map[string]any{
				"npm": "@ai-sdk/openai-compatible",
				"options": map[string]any{
					"baseURL": server.URL,
				},
			},
		},
	})
	config := readGeneratedConfig(t, server.Client(), configDir, filepath.Join(t.TempDir(), "Projects"), testInstructions)

	models := config["provider"].(map[string]any)["local"].(map[string]any)["models"].(map[string]any)
	if _, ok := models["alpha"]; !ok {
		t.Fatalf("models = %#v, want alpha", models)
	}
	if _, ok := models["beta"]; !ok {
		t.Fatalf("models = %#v, want beta", models)
	}
}

func TestGeneratedConfigReturnsModelFetchWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	configDir := t.TempDir()
	writeJSON(t, filepath.Join(configDir, "opencode.json"), map[string]any{
		"provider": map[string]any{
			"local": map[string]any{
				"npm":     "@ai-sdk/openai-compatible",
				"options": map[string]any{"baseURL": server.URL},
			},
		},
	})
	_, warnings, err := staticFiles(t, server.Client(), configDir, filepath.Join(t.TempDir(), "Projects"), testInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", warnings)
	}
}

func TestGeneratedCommandIsExposed(t *testing.T) {
	files, warnings, err := staticFiles(t, &http.Client{}, t.TempDir(), filepath.Join(t.TempDir(), "Projects"), testProjectInstructions, WithMountableProjects(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	file := findStaticFile(files, StaticProjectMountPath)
	if file == nil {
		t.Fatalf("files = %#v, want %s", files, StaticProjectMountPath)
	}
	if string(file.Data) != string(projectMountCommand) {
		t.Fatalf("command = %q", file.Data)
	}
}

func TestGeneratedCommandIsHiddenWithoutMountableProjects(t *testing.T) {
	files, warnings, err := staticFiles(t, &http.Client{}, t.TempDir(), filepath.Join(t.TempDir(), "Projects"), testInstructions)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if findStaticFile(files, StaticProjectMountPath) != nil {
		t.Fatalf("files = %#v, want project mount command hidden", files)
	}
}

func TestGeneratedFilesAreReadOnly(t *testing.T) {
	files, warnings, err := staticFiles(t, &http.Client{}, t.TempDir(), filepath.Join(t.TempDir(), "Projects"), testProjectInstructions, WithMountableProjects(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for _, path := range []string{StaticGitignorePath, StaticConfigPath, StaticProjectMountPath} {
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

func readGeneratedConfig(t *testing.T, client *http.Client, configDir, projectRoot string, instructions []string, opts ...MountOption) map[string]any {
	t.Helper()
	files, warnings, err := staticFiles(t, client, configDir, projectRoot, instructions, opts...)
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

func staticFiles(t *testing.T, client *http.Client, configDir, projectRoot string, instructions []string, opts ...MountOption) ([]staticmount.File, []error, error) {
	t.Helper()
	renderer, service := testDeps(t, client)
	builder := service.NewBuilder()
	warnings, err := renderer.RegisterStaticFiles(context.Background(), builder, configDir, projectRoot, instructions, opts...)
	if err != nil {
		return nil, warnings, err
	}
	return builder.Files(), warnings, nil
}

func testDeps(t *testing.T, client *http.Client) (*Renderer, *staticfiles.Service) {
	t.Helper()
	var renderer *Renderer
	var service *staticfiles.Service
	app := fxtest.New(t,
		fx.Supply(client),
		fx.Provide(NewRenderer, staticfiles.NewService),
		fx.Populate(&renderer, &service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	return renderer, service
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

func findStaticFile(files []staticmount.File, path string) *staticmount.File {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}
