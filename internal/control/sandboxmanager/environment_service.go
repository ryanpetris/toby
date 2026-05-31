package sandboxmanager

import (
	"context"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type EnvironmentServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.manager.services"`
}

type EnvironmentService struct{}

func NewEnvironmentService() EnvironmentServiceResult {
	return EnvironmentServiceResult{Service: EnvironmentService{}}
}

func (EnvironmentService) Commands() []Command {
	return []Command{
		CommandFunc{Name: control.MethodEnvironmentGet, Run: handleEnvironmentGet},
		CommandFunc{Name: control.MethodEnvironmentSet, Run: handleEnvironmentSet},
	}
}

func handleEnvironmentGet(_ context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	return control.ResponseOK(req.ID, control.EnvironmentGetResult{Environment: runtime.environmentSnapshot()}), nil
}

func handleEnvironmentSet(_ context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeEnvironmentSetParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	runtime.setEnvironment(params.Name, params.Value)
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
