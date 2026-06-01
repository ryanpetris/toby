package helpers

import (
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/tools/tool"
)

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

func TestDefaultSandboxPaths(t *testing.T) {
	paths := DefaultSandboxPaths()
	if paths.Root != tool.DefaultSandboxRoot || paths.Home != tool.DefaultSandboxHome || paths.Context != tool.DefaultSandboxContext || paths.Bin != tool.DefaultSandboxBin || paths.Workspace != tool.DefaultSandboxWorkspace {
		t.Fatalf("paths = %#v", paths)
	}
}

type pathSandbox struct {
	home     string
	runtime  string
	projects string
}

func (s pathSandbox) Paths() tool.SandboxPaths {
	return tool.SandboxPaths{Root: s.runtime, Home: s.home, Context: filepath.Join(s.runtime, "context"), Bin: filepath.Join(s.runtime, "bin"), Workspace: s.projects}
}

func (s pathSandbox) HomeDir() string        { return s.home }
func (s pathSandbox) Projects() string       { return s.projects }
func (s pathSandbox) TobyRuntimeDir() string { return s.runtime }
func (s pathSandbox) TobyContextDir() string { return filepath.Join(s.runtime, "context") }
func (s pathSandbox) TobyOpenCodeConfigDir() string {
	return filepath.Join(s.runtime, "context", "opencode")
}
