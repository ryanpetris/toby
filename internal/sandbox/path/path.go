package path

import "path/filepath"

type Base string

const (
	Absolute Base = "absolute"
	Root     Base = "root"
	Home     Base = "home"
	Runtime  Base = "runtime"
	Context  Base = "context"
	Bin      Base = "bin"
	Projects Base = "projects"

	DefaultRoot      = "/toby"
	DefaultHome      = "/toby/home"
	DefaultContext   = "/toby/context"
	DefaultBin       = "/toby/bin"
	DefaultWorkspace = "/toby/workspace"
)

type Paths struct {
	Root      string
	Home      string
	Context   string
	Bin       string
	Workspace string
}

type Target struct {
	Base Base
	Path string
}

func Defaults() Paths {
	return Paths{
		Root:      DefaultRoot,
		Home:      DefaultHome,
		Context:   DefaultContext,
		Bin:       DefaultBin,
		Workspace: DefaultWorkspace,
	}
}

func AbsolutePath(value string) Target { return Target{Base: Absolute, Path: value} }

func RootPath(parts ...string) Target { return target(Root, parts...) }

func HomePath(parts ...string) Target { return target(Home, parts...) }

func RuntimePath(parts ...string) Target { return target(Runtime, parts...) }

func ContextPath(parts ...string) Target { return target(Context, parts...) }

func BinPath(parts ...string) Target { return target(Bin, parts...) }

func ProjectsPath(parts ...string) Target { return target(Projects, parts...) }

func Resolve(target Target, paths Paths) string {
	switch target.Base {
	case Root:
		return join(paths.Root, target.Path)
	case Home:
		return join(paths.Home, target.Path)
	case Runtime:
		return join(paths.Root, target.Path)
	case Context:
		return join(paths.Context, target.Path)
	case Bin:
		return join(paths.Bin, target.Path)
	case Projects:
		return join(paths.Workspace, target.Path)
	case Absolute, "":
		return target.Path
	default:
		return target.Path
	}
}

func target(base Base, parts ...string) Target {
	if len(parts) == 0 {
		return Target{Base: base}
	}
	return Target{Base: base, Path: filepath.ToSlash(filepath.Join(parts...))}
}

func join(base, rel string) string {
	if rel == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}
