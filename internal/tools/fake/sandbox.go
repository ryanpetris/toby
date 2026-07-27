// Package fake provides test doubles for exercising native tool implementations.
package fake

import (
	"context"
	"strings"

	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// Sandbox records sandbox mutations and delegates command execution to ExecFunc.
type Sandbox struct {
	Env      map[string]string
	Binds    []mount.Bind
	Mounts   []mount.Request
	ExecFunc func(context.Context, []string, sandbox.ExecOptions) (int, error)
}

var _ sandbox.Service = (*Sandbox)(nil)

// NewSandbox returns an empty recording sandbox.
func NewSandbox() *Sandbox {
	return &Sandbox{Env: map[string]string{}}
}

// ProjectPath returns the configured project path.
func (s *Sandbox) ProjectPath(string) (string, bool) { return "", false }

// VisibleHostPath resolves a host path visible to the sandbox.
func (s *Sandbox) VisibleHostPath(string) (string, error) { return "", nil }

// Environment returns a copy of the configured environment.
func (s *Sandbox) Environment(name string) (string, bool) {
	value, ok := s.Env[name]
	return value, ok
}

// SetEnvironment sets an environment variable.
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

// PrependEnvironment prepends a value to an environment variable.
func (s *Sandbox) PrependEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setPathEntry(ctx, name, value, separator, true)
}

// AppendEnvironment appends a value to an environment variable.
func (s *Sandbox) AppendEnvironment(ctx context.Context, name, value, separator string) error {
	return s.setPathEntry(ctx, name, value, separator, false)
}

// AddBind records a host bind.
func (s *Sandbox) AddBind(bind mount.Bind) error {
	bind.Target = layout.ExpandHome(bind.Target)
	s.Binds = append(s.Binds, bind)
	return nil
}

// AddMount records a managed volume.
func (s *Sandbox) AddMount(request mount.Request) error {
	if request.Access == "" {
		request.Access = mount.AccessRegular
	}
	request.Target = layout.ExpandHome(request.Target)
	s.Mounts = append(s.Mounts, request)
	return nil
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

// Exec records and optionally handles a sandbox command.
func (s *Sandbox) Exec(ctx context.Context, argv []string, opts sandbox.ExecOptions) (int, error) {
	if s.ExecFunc != nil {
		return s.ExecFunc(ctx, argv, opts)
	}
	return 0, nil
}
