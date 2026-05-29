package hostmanager

import (
	"context"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type ContextServiceResult struct {
	fx.Out

	Service Service `group:"toby.manager.services"`
}

type ContextService struct{}

func NewContextService() ContextServiceResult {
	return ContextServiceResult{Service: ContextService{}}
}

func (ContextService) Commands() []Command {
	return []Command{CommandFunc{Name: control.MethodContextInit, Run: handleContextInit}}
}

func handleContextInit(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	manager := runtime.Manager
	var err error
	if manager.ContextInit == nil {
		err = syscall.ENOSYS
	} else {
		err = manager.ContextInit(ctx, runtime.Sandbox)
	}
	if manager.SandboxReady != nil {
		manager.SandboxReady(runtime.Sandbox, err)
	}
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), err
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
