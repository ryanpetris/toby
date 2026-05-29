package claudeconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"petris.dev/toby/internal/staticfiles"
	"petris.dev/toby/internal/staticmount"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func fileByPath(t *testing.T, files []staticmount.File, path string) staticmount.File {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("static file %q not found", path)
	return staticmount.File{}
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
	files, err := renderStaticFiles(t, "/home/toby/Projects/app", [][]byte{[]byte("# git")}, false)
	if err != nil {
		t.Fatal(err)
	}

	mcp := decode(t, fileByPath(t, files, StaticMcpPath).Data)
	toby := mcp["mcpServers"].(map[string]any)["toby"].(map[string]any)
	if toby["type"] != "stdio" || toby["command"] != "toby" {
		t.Fatalf("mcp.toby = %#v", toby)
	}
	if args := toby["args"].([]any); len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("mcp.toby.args = %#v", args)
	}
}

func TestStaticFilesIncludesPermissionDirectories(t *testing.T) {
	projectRoot := "/home/toby/Projects/app"
	files, err := renderStaticFiles(t, projectRoot, [][]byte{[]byte("# git")}, false)
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
	files, err := renderStaticFiles(t, "/p", [][]byte{[]byte("# git\n"), []byte("# mount\n")}, true)
	if err != nil {
		t.Fatal(err)
	}
	got := string(fileByPath(t, files, StaticInstructionsPath).Data)
	if !strings.Contains(got, "# git") || !strings.Contains(got, "# mount") {
		t.Fatalf("instructions = %q", got)
	}
}

func TestStaticFilesPluginOnlyWhenMountable(t *testing.T) {
	without, err := renderStaticFiles(t, "/p", [][]byte{[]byte("# git")}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range without {
		if file.Path == StaticProjectMountCommandPath || file.Path == StaticPluginManifestPath {
			t.Fatalf("unexpected plugin file without mountable projects: %q", file.Path)
		}
	}

	with, err := renderStaticFiles(t, "/p", [][]byte{[]byte("# git")}, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest := decode(t, fileByPath(t, with, StaticPluginManifestPath).Data)
	if manifest["name"] != "toby" {
		t.Fatalf("plugin manifest name = %#v", manifest["name"])
	}
	cmd := string(fileByPath(t, with, StaticProjectMountCommandPath).Data)
	if !strings.Contains(cmd, "project_mount") {
		t.Fatalf("project mount command = %q", cmd)
	}
}

func renderStaticFiles(t *testing.T, projectRoot string, instructions [][]byte, mountableProjects bool) ([]staticmount.File, error) {
	t.Helper()
	var service *staticfiles.Service
	app := fxtest.New(t,
		fx.Provide(staticfiles.NewService),
		fx.Populate(&service),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	builder := service.NewBuilder()
	if err := RegisterStaticFiles(builder, projectRoot, instructions, mountableProjects); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}
