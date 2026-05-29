package claudeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"petris.dev/toby/internal/staticfiles"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func fileByPath(t *testing.T, files []staticfiles.File, path string) staticfiles.File {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("static file %q not found", path)
	return staticfiles.File{}
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return value
}

func TestStaticFilesIncludesTobyMCPServer(t *testing.T) {
	files, err := renderStaticFiles(t, "/home/toby/Projects/app", [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}

	mcp := decode(t, fileByPath(t, files, StaticMcpPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "stdio" || toby["command"] != "toby-sandbox" {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	if args := toby["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("mcp.toby.args = %#v", args)
	}
}

func TestStaticFilesIncludesPermissionDirectories(t *testing.T) {
	projectRoot := "/home/toby/Projects/app"
	files, err := renderStaticFiles(t, projectRoot, [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}

	settings := decode(t, fileByPath(t, files, StaticSettingsPath).Data)
	dirs := settings["permissions"].(map[string]any)["additionalDirectories"].([]any)
	want := map[string]bool{"/tmp": false, projectRoot: false}
	for _, dir := range dirs {
		if _, ok := want[dir.(string)]; ok {
			want[dir.(string)] = true
		}
	}
	for dir, seen := range want {
		if !seen {
			t.Fatalf("additionalDirectories missing %q: %#v", dir, dirs)
		}
	}
}

func TestStaticFilesCombinesInstructions(t *testing.T) {
	files, err := renderStaticFiles(t, "/p", [][]byte{[]byte("# git\n"), []byte("# context\n")})
	if err != nil {
		t.Fatal(err)
	}
	got := string(fileByPath(t, files, StaticInstructionsPath).Data)
	if !strings.Contains(got, "# git") || !strings.Contains(got, "# context") {
		t.Fatalf("instructions = %q", got)
	}
}

func TestStaticFilesDoNotIncludePlugin(t *testing.T) {
	files, err := renderStaticFiles(t, "/p", [][]byte{[]byte("# git")})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, "claude/plugin/") {
			t.Fatalf("unexpected plugin file: %q", file.Path)
		}
	}
}

func renderStaticFiles(t *testing.T, projectRoot string, instructions [][]byte) ([]staticfiles.File, error) {
	t.Helper()
	var service *staticfiles.Service
	app := fxtest.New(t,
		fx.Provide(staticfiles.NewService),
		fx.Populate(&service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	builder := service.NewBuilder()
	if err := RegisterStaticFiles(builder, projectRoot, instructions); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}
