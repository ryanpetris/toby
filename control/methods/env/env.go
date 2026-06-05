// Package env implements the env.get / env.set control methods and owns the
// sandbox manager's authoritative environment map. The Service is registered both
// into the sandbox handler group (so it answers env.* requests) and as a concrete
// *Service, so capabilities that need the environment (e.g. command) inject it
// directly.
package env

import (
	"context"
	"os"
	"strings"
	"sync"
	"syscall"

	"petris.dev/toby/control"
)

// Service holds the sandbox environment map and answers env.* methods.
type Service struct {
	mu     sync.Mutex
	values map[string]string
}

var _ control.Capability = (*Service)(nil)

// New constructs the Service seeded from the current process environment.
func New() *Service {
	return &Service{values: fromList(os.Environ())}
}

// Methods reports the env.* methods this capability handles.
func (s *Service) Methods() []control.Method {
	return []control.Method{
		{Name: MethodGet, Handle: s.handleGet},
		{Name: MethodSet, Handle: s.handleSet},
	}
}

func (s *Service) handleGet(_ context.Context, req control.RPCRequest) ([]byte, error) {
	return control.ResponseOK(req.ID, GetResult{Environment: s.Snapshot()}), nil
}

func (s *Service) handleSet(_ context.Context, req control.RPCRequest) ([]byte, error) {
	params, err := DecodeSetParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	s.Set(params.Name, params.Value)
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

// Snapshot returns a copy of the full environment map.
func (s *Service) Snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.values)
}

// Get returns a single variable.
func (s *Service) Get(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[name]
	return value, ok
}

// Set assigns a variable; an empty value unsets it.
func (s *Service) Set(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]string{}
	}
	if value == "" {
		delete(s.values, name)
		return
	}
	s.values[name] = value
}

// List returns the full environment as name=value entries.
func (s *Service) List() []string {
	return toList(s.Snapshot())
}

// CommandEnvironment returns the environment a spawned command should see: the
// full map minus the control-endpoint variables, which must not leak to commands.
func (s *Service) CommandEnvironment() map[string]string {
	env := s.Snapshot()
	delete(env, control.EnvControlHost)
	delete(env, control.EnvControlToken)
	return env
}

// CommandEnvironmentList is CommandEnvironment as name=value entries.
func (s *Service) CommandEnvironmentList() []string {
	return toList(s.CommandEnvironment())
}

func toList(env map[string]string) []string {
	values := make([]string, 0, len(env))
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	return values
}

func fromList(values []string) map[string]string {
	env := make(map[string]string, len(values))
	for _, item := range values {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	return env
}

func clone(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for name, value := range env {
		out[name] = value
	}
	return out
}
