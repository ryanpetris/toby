package bwrap

// Provides the process-local sandbox facade used by the static Fx tool graph.

import (
	"context"
	"fmt"
	"sync"

	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/mount"
)

// ToolService is the stable process-local sandbox.Service injected into tools.
// A launch attaches its run-local ToolSandbox after resolving OCI and project
// state, then clears it during launch cleanup.
type ToolService struct {
	mu      sync.RWMutex
	current sandboxapi.Service
}

var _ sandboxapi.Service = (*ToolService)(nil)

// NewToolService creates an unattached tool sandbox facade.
func NewToolService() *ToolService {
	return &ToolService{}
}

// Set attaches service for the process's active launch.
func (s *ToolService) Set(service sandboxapi.Service) error {
	if service == nil {
		return fmt.Errorf("set tool sandbox: service is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		return fmt.Errorf("set tool sandbox: a launch is already attached")
	}
	s.current = service
	return nil
}

// Clear detaches service. The expected value prevents stale cleanup from
// clearing a newer attachment.
func (s *ToolService) Clear(expected sandboxapi.Service) error {
	if expected == nil {
		return fmt.Errorf("clear tool sandbox: expected service is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return fmt.Errorf("clear tool sandbox: no launch is attached")
	}
	if s.current != expected {
		return fmt.Errorf("clear tool sandbox: attached service does not match")
	}
	s.current = nil
	return nil
}

func (s *ToolService) service() (sandboxapi.Service, error) {
	if s == nil {
		return nil, fmt.Errorf("tool sandbox facade is nil")
	}

	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current == nil {
		return nil, fmt.Errorf("tool sandbox is not attached to a launch")
	}
	return current, nil
}

// ProjectPath returns the configured project path.
func (s *ToolService) ProjectPath(name string) (string, bool) {
	service, err := s.service()
	if err != nil {
		return "", false
	}
	return service.ProjectPath(name)
}

// VisibleHostPath resolves a host path visible to the sandbox.
func (s *ToolService) VisibleHostPath(repository string) (string, error) {
	service, err := s.service()
	if err != nil {
		return "", err
	}
	return service.VisibleHostPath(repository)
}

// Environment returns a copy of the configured environment.
func (s *ToolService) Environment(name string) (string, bool) {
	service, err := s.service()
	if err != nil {
		return "", false
	}
	return service.Environment(name)
}

// SetEnvironment sets an environment variable.
func (s *ToolService) SetEnvironment(
	ctx context.Context,
	name string,
	value string,
) error {
	service, err := s.service()
	if err != nil {
		return err
	}
	return service.SetEnvironment(ctx, name, value)
}

// PrependEnvironment prepends a value to an environment variable.
func (s *ToolService) PrependEnvironment(
	ctx context.Context,
	name string,
	value string,
	separator string,
) error {
	service, err := s.service()
	if err != nil {
		return err
	}
	return service.PrependEnvironment(ctx, name, value, separator)
}

// AppendEnvironment appends a value to an environment variable.
func (s *ToolService) AppendEnvironment(
	ctx context.Context,
	name string,
	value string,
	separator string,
) error {
	service, err := s.service()
	if err != nil {
		return err
	}
	return service.AppendEnvironment(ctx, name, value, separator)
}

// AddBind adds a host bind through the current sandbox service.
func (s *ToolService) AddBind(bind mount.Bind) error {
	service, err := s.service()
	if err != nil {
		return err
	}
	return service.AddBind(bind)
}

// AddMount adds a managed volume through the current sandbox service.
func (s *ToolService) AddMount(request mount.Request) error {
	service, err := s.service()
	if err != nil {
		return err
	}
	return service.AddMount(request)
}

// Exec executes a command through the current sandbox service.
func (s *ToolService) Exec(
	ctx context.Context,
	argv []string,
	options sandboxapi.ExecOptions,
) (int, error) {
	service, err := s.service()
	if err != nil {
		return 1, err
	}
	return service.Exec(ctx, argv, options)
}
