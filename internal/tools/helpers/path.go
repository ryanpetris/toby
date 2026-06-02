package helpers

import (
	"path/filepath"

	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/tool"
)

func AbsoluteTarget(path string) sandboxpath.Target { return sandboxpath.AbsolutePath(path) }

func RootTarget(parts ...string) sandboxpath.Target { return sandboxpath.RootPath(parts...) }

func HomeTarget(parts ...string) sandboxpath.Target { return sandboxpath.HomePath(parts...) }

func RuntimeTarget(parts ...string) sandboxpath.Target { return sandboxpath.RuntimePath(parts...) }

func ContextTarget(parts ...string) sandboxpath.Target { return sandboxpath.ContextPath(parts...) }

func BinTarget(parts ...string) sandboxpath.Target { return sandboxpath.BinPath(parts...) }

func ProjectsTarget(parts ...string) sandboxpath.Target { return sandboxpath.ProjectsPath(parts...) }

func ResolvePath(target sandboxpath.Target, sandbox tool.Sandbox) string {
	return sandboxpath.Resolve(target, sandbox.Paths())
}

func HomePath(home string, parts ...string) string {
	items := append([]string{home}, parts...)
	return filepath.Join(items...)
}
