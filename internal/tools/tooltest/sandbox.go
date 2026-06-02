package tooltest

import (
	"context"
	"path/filepath"
	"strings"

	contextfiles "petris.dev/toby/internal/context/files"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/tool"
)

type Sandbox struct {
	PathsValue sandboxpath.Paths
	Env        map[string]string
	Files      []contextfiles.File
	Dirs       []string
	Binds      []sandboxmount.Bind
	Mounts     []sandboxmount.Info
	Symlinks   map[string]string
	ExecFunc   func(context.Context, []string, tool.ExecOptions) (int, error)
	MCPURL     string
}

func NewSandbox(contextDir string) *Sandbox {
	root := filepath.Dir(contextDir)
	return &Sandbox{
		PathsValue: sandboxpath.Paths{Root: root, Home: filepath.Dir(root), Context: contextDir, Bin: filepath.Join(root, "bin"), Workspace: filepath.Join(filepath.Dir(root), "Projects")},
		Env:        map[string]string{},
		Symlinks:   map[string]string{},
	}
}

func (s *Sandbox) Paths() sandboxpath.Paths               { return s.PathsValue }
func (s *Sandbox) ProjectPath(string) (string, bool)      { return "", false }
func (s *Sandbox) VisibleHostPath(string) (string, error) { return "", nil }
func (s *Sandbox) GetEnvironment(name string) (string, bool) {
	value, ok := s.Env[name]
	return value, ok
}
func (s *Sandbox) SetEnvironment(_ context.Context, name, value string) error {
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	if value == "" {
		delete(s.Env, name)
	} else {
		s.Env[name] = value
	}
	return nil
}
func (s *Sandbox) PrependEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setPathEntry(ctx, name, value, separator, true)
}
func (s *Sandbox) AppendEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setPathEntry(ctx, name, value, separator, false)
}

func (s *Sandbox) AddBind(bind sandboxmount.Bind) error {
	s.Binds = append(s.Binds, bind)
	return nil
}

func (s *Sandbox) AddMount(req sandboxmount.Request) (sandboxmount.Info, error) {
	info := sandboxmount.Info{Key: req.Key, Profile: "test", ProviderID: sandboxmount.ProviderID("test", req.Key), Backing: sandboxmount.BackingProvider, Target: sandboxpath.Resolve(req.Target, s.Paths()), Subpath: req.Subpath, Active: true, Source: sandboxmount.Source{Kind: sandboxmount.SourceProvider, Value: sandboxmount.ProviderID("test", req.Key)}, SetupPath: filepath.ToSlash(filepath.Join(sandboxpath.DefaultRoot, "mounts", "test-"+req.Key.Type+"-"+req.Key.Name+"-"+req.Key.Purpose)), Access: req.Access, Optional: req.Optional}
	s.Mounts = append(s.Mounts, info)
	return info, nil
}

func (s *Sandbox) Mount(key sandboxmount.Key) (sandboxmount.Info, bool) {
	for _, item := range s.Mounts {
		if item.Key == key {
			return item, true
		}
	}
	return sandboxmount.Info{}, false
}

func (s *Sandbox) setPathEntry(ctx context.Context, name, value, separator string, atStart bool) error {
	if separator == "" {
		separator = ":"
	}
	parts := strings.Split(s.Env[name], separator)
	entries := make([]string, 0, len(parts)+1)
	if atStart {
		entries = append(entries, value)
	}
	for _, part := range parts {
		if part == "" || part == value {
			continue
		}
		entries = append(entries, part)
	}
	if !atStart {
		entries = append(entries, value)
	}
	return s.SetEnvironment(ctx, name, strings.Join(entries, separator))
}
func (s *Sandbox) AddFile(_ context.Context, path string, data []byte, mode uint32) error {
	rel := path
	if s.PathsValue.Context != "" {
		rel = strings.TrimPrefix(path, s.PathsValue.Context+string(filepath.Separator))
	}
	s.Files = append(s.Files, contextfiles.File{Path: filepath.ToSlash(rel), Data: append([]byte(nil), data...), Mode: mode})
	return nil
}
func (s *Sandbox) AddFileOwned(ctx context.Context, path string, data []byte, mode uint32, _, _ int) error {
	return s.AddFile(ctx, path, data, mode)
}
func (s *Sandbox) DeletePath(context.Context, string, bool) error { return nil }
func (s *Sandbox) Mkdir(_ context.Context, path string, _ uint32) error {
	s.Dirs = append(s.Dirs, path)
	return nil
}
func (s *Sandbox) MkdirOwned(ctx context.Context, path string, mode uint32, _, _ int) error {
	return s.Mkdir(ctx, path, mode)
}
func (s *Sandbox) Symlink(_ context.Context, path, target string) error {
	if s.Symlinks == nil {
		s.Symlinks = map[string]string{}
	}
	s.Symlinks[path] = target
	return nil
}
func (s *Sandbox) SymlinkOwned(ctx context.Context, path, target string, _, _ int) error {
	return s.Symlink(ctx, path, target)
}
func (s *Sandbox) Exec(ctx context.Context, argv []string, opts tool.ExecOptions) (int, error) {
	if s.ExecFunc != nil {
		return s.ExecFunc(ctx, argv, opts)
	}
	return 0, nil
}
func (s *Sandbox) TobyMCPURL() string { return s.MCPURL }
