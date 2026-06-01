package helpers

import (
	"path/filepath"

	"petris.dev/toby/internal/tools/tool"
)

func DefaultSandboxPaths() tool.SandboxPaths {
	return tool.SandboxPaths{
		Root:      tool.DefaultSandboxRoot,
		Home:      tool.DefaultSandboxHome,
		Context:   tool.DefaultSandboxContext,
		Bin:       tool.DefaultSandboxBin,
		Workspace: tool.DefaultSandboxWorkspace,
	}
}

func AbsoluteTarget(path string) tool.PathTarget {
	return tool.PathTarget{Base: tool.PathAbsolute, Path: path}
}

func RootTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathRoot, parts...) }

func HomeTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathHome, parts...) }

func RuntimeTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathRuntime, parts...) }

func ContextTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathContext, parts...) }

func BinTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathBin, parts...) }

func ProjectsTarget(parts ...string) tool.PathTarget { return pathTarget(tool.PathProjects, parts...) }

func pathTarget(base tool.PathBase, parts ...string) tool.PathTarget {
	if len(parts) == 0 {
		return tool.PathTarget{Base: base}
	}
	return tool.PathTarget{Base: base, Path: filepath.ToSlash(filepath.Join(parts...))}
}

func ResolvePath(target tool.PathTarget, sandbox tool.Sandbox) string {
	switch target.Base {
	case tool.PathRoot:
		return joinSandboxPath(sandbox.Paths().Root, target.Path)
	case tool.PathHome:
		return joinSandboxPath(sandbox.Paths().Home, target.Path)
	case tool.PathRuntime:
		return joinSandboxPath(sandbox.Paths().Root, target.Path)
	case tool.PathContext:
		return joinSandboxPath(sandbox.Paths().Context, target.Path)
	case tool.PathBin:
		return joinSandboxPath(sandbox.Paths().Bin, target.Path)
	case tool.PathProjects:
		return joinSandboxPath(sandbox.Paths().Workspace, target.Path)
	case tool.PathAbsolute, "":
		return target.Path
	default:
		return target.Path
	}
}

func ResolveStateBindHostPath(root string, bind tool.Bind) string {
	statePath := bind.StatePath
	if statePath == "" && bind.Target.Base == tool.PathHome {
		statePath = bind.Target.Path
	}
	if root == "" || statePath == "" {
		return bind.HostPath
	}
	return filepath.Join(root, filepath.FromSlash(statePath))
}

func HomePath(home string, parts ...string) string {
	items := append([]string{home}, parts...)
	return filepath.Join(items...)
}

func joinSandboxPath(base, rel string) string {
	if rel == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}
