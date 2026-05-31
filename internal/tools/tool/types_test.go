package tool

import (
	"path/filepath"
	"testing"
)

func TestParseToolState(t *testing.T) {
	if state, err := ParseToolState(" host "); err != nil || state != ToolStateHost {
		t.Fatalf("ParseToolState host = %q, %v", state, err)
	}
	if state, err := ParseToolState("private"); err != nil || state != ToolStatePrivate {
		t.Fatalf("ParseToolState private = %q, %v", state, err)
	}
	if _, err := ParseToolState("shared"); err == nil {
		t.Fatal("expected invalid state to fail")
	}
}

func TestToolStateSettingsMergeStateForAndResolveRoots(t *testing.T) {
	settings := ToolStateSettings{
		Default: ToolStateConfig{State: ToolStatePrivate, StateRoot: "~/default"},
		Tools: map[string]ToolStateConfig{
			OpenCodeToolName: {State: ToolStateHost},
		},
	}
	settings.Merge(ToolStateSettings{Tools: map[string]ToolStateConfig{
		OpenCodeToolName: {StateRoot: "state/opencode"},
		ClaudeToolName:   {State: ToolStateHost},
	}})

	if settings.StateFor(NpmToolName) != ToolStatePrivate || settings.StateFor(OpenCodeToolName) != ToolStateHost || settings.StateFor(DockerToolName) != ToolStatePrivate || (ToolStateSettings{}).StateFor(DockerToolName) != ToolStateHost {
		t.Fatalf("states = %#v", settings)
	}
	if settings.StateRootFor(OpenCodeToolName) != "state/opencode" || settings.StateRootFor(ClaudeToolName) != "~/default" {
		t.Fatalf("state roots = %#v", settings)
	}

	home := filepath.Join(string(filepath.Separator), "home", "demo")
	base := filepath.Join(home, "project")
	resolved, err := settings.ResolveStateRoots(home, base)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Default.StateRoot != filepath.Join(home, "default") || resolved.Tools[OpenCodeToolName].StateRoot != filepath.Join(base, "state", "opencode") {
		t.Fatalf("resolved = %#v", resolved)
	}
	if settings.Default.StateRoot != "~/default" {
		t.Fatalf("ResolveStateRoots mutated source: %#v", settings)
	}
}

func TestResolveStateRoot(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "demo")
	base := filepath.Join(home, "project")
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "home", value: "~/state", want: filepath.Join(home, "state")},
		{name: "absolute", value: "/tmp/state", want: "/tmp/state"},
		{name: "relative", value: "state", want: filepath.Join(base, "state")},
		{name: "empty", value: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveStateRoot(tt.value, home, base)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ResolveStateRoot = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestPathTargetsAndResolvePath(t *testing.T) {
	sandbox := pathSandbox{
		home:     filepath.Join(string(filepath.Separator), "home", "demo"),
		runtime:  filepath.Join(string(filepath.Separator), "run", "toby"),
		projects: filepath.Join(string(filepath.Separator), "home", "demo", "Projects"),
	}
	if target := HomeTarget(".config", "toby"); target.Path != ".config/toby" || ResolvePath(target, sandbox) != filepath.Join(sandbox.home, ".config", "toby") {
		t.Fatalf("home target = %#v", target)
	}
	if target := RuntimeTarget("bin"); ResolvePath(target, sandbox) != filepath.Join(sandbox.runtime, "bin") {
		t.Fatalf("runtime target = %#v", target)
	}
	if target := RootTarget("tmp"); ResolvePath(target, sandbox) != filepath.Join(sandbox.runtime, "tmp") {
		t.Fatalf("root target = %#v", target)
	}
	if target := ContextTarget("npm", "sandbox-init"); ResolvePath(target, sandbox) != filepath.Join(sandbox.runtime, "context", "npm", "sandbox-init") {
		t.Fatalf("context target = %#v", target)
	}
	if target := BinTarget("toby"); ResolvePath(target, sandbox) != filepath.Join(sandbox.runtime, "bin", "toby") {
		t.Fatalf("bin target = %#v", target)
	}
	if target := ProjectsTarget("app"); ResolvePath(target, sandbox) != filepath.Join(sandbox.projects, "app") {
		t.Fatalf("projects target = %#v", target)
	}
	if got := ResolvePath(AbsoluteTarget("/tmp/file"), sandbox); got != "/tmp/file" {
		t.Fatalf("absolute path = %q", got)
	}
}

type pathSandbox struct {
	home     string
	runtime  string
	projects string
}

func (s pathSandbox) Paths() SandboxPaths {
	return SandboxPaths{Root: s.runtime, Home: s.home, Context: filepath.Join(s.runtime, "context"), Bin: filepath.Join(s.runtime, "bin"), Workspace: s.projects}
}

func (s pathSandbox) HomeDir() string        { return s.home }
func (s pathSandbox) Projects() string       { return s.projects }
func (s pathSandbox) TobyRuntimeDir() string { return s.runtime }
func (s pathSandbox) TobyContextDir() string { return filepath.Join(s.runtime, "context") }
func (s pathSandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.runtime, "context", "opencode")
}
