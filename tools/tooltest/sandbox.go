// Package tooltest provides test doubles for exercising tool implementations,
// chiefly a fake sandbox.Service that records mounts, files, and environment
// changes in memory instead of touching a real sandbox.
package tooltest

import (
	"context"
	"path/filepath"
	"strings"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
)

type Sandbox struct {
	Env      map[string]string
	Files    []contextfiles.File
	Dirs     []string
	Binds    []mount.Bind
	Mounts   []mount.Entry
	Symlinks map[string]string
	ExecFunc func(context.Context, []string, sandbox.ExecOptions) (int, error)
	MCPURL   string
}

func NewSandbox(string) *Sandbox {
	return &Sandbox{
		Env:      map[string]string{},
		Symlinks: map[string]string{},
	}
}

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

func (s *Sandbox) AddBind(bind mount.Bind) error {
	bind.Target = layout.Expand(bind.Target)
	s.Binds = append(s.Binds, bind)
	return nil
}

func (s *Sandbox) AddMount(req mount.Request) (mount.Entry, error) {
	access := req.Access
	if access == "" {
		access = mount.AccessRegular
	}
	m := mount.Entry{
		Key:       req.Key,
		Profile:   "test",
		Volume:    mount.Volume("test", req.Key),
		Target:    layout.Expand(req.Target),
		Access:    access,
		Optional:  req.Optional,
		SetupPath: "/toby/mounts/test-" + req.Key.Type + "-" + req.Key.Name + "-" + req.Key.Purpose,
	}
	s.Mounts = append(s.Mounts, m)
	return m, nil
}

func (s *Sandbox) Mount(key mount.Key) (mount.Entry, bool) {
	for _, item := range s.Mounts {
		if item.Key == key {
			return item, true
		}
	}
	return mount.Entry{}, false
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
	rel := strings.TrimPrefix(path, layout.Context+string(filepath.Separator))
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
func (s *Sandbox) Exec(ctx context.Context, argv []string, opts sandbox.ExecOptions) (int, error) {
	if s.ExecFunc != nil {
		return s.ExecFunc(ctx, argv, opts)
	}
	return 0, nil
}
func (s *Sandbox) TobyMCPURL() string { return s.MCPURL }
