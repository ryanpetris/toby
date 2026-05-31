package hostmanager

import (
	"context"
	"fmt"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

const FxServiceGroup = "toby.manager.services"

type Service interface {
	Commands() []Command
}

type Command interface {
	Method() string
	Handle(context.Context, *Runtime, control.RPCRequest) ([]byte, error)
}

type CommandFunc struct {
	Name string
	Run  func(context.Context, *Runtime, control.RPCRequest) ([]byte, error)
}

func (c CommandFunc) Method() string { return c.Name }

func (c CommandFunc) Handle(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	return c.Run(ctx, runtime, req)
}

type RegistryParams struct {
	fx.In

	Services []Service `group:"toby.manager.services"`
}

type Registry struct {
	commands map[string]Command
}

func NewRegistry(params RegistryParams) (*Registry, error) {
	registry := &Registry{commands: map[string]Command{}}
	for _, service := range params.Services {
		if service == nil {
			continue
		}
		for _, command := range service.Commands() {
			if command == nil {
				continue
			}
			method := command.Method()
			if method == "" {
				return nil, fmt.Errorf("host manager command must define a method")
			}
			if _, exists := registry.commands[method]; exists {
				return nil, fmt.Errorf("duplicate host manager command: %s", method)
			}
			registry.commands[method] = command
		}
	}
	return registry, nil
}

func (r *Registry) Handle(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	if r == nil {
		return control.ResponseError(req.ID, control.CodeInternalError, "host manager command registry is not configured", nil), syscall.ENOSYS
	}
	command, ok := r.commands[req.Method]
	if !ok {
		return control.ResponseError(req.ID, control.CodeMethodNotFound, "method not found: "+req.Method, nil), syscall.ENOSYS
	}
	return command.Handle(ctx, runtime, req)
}
