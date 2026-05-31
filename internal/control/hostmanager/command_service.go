package hostmanager

import (
	"context"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type CommandServiceResult struct {
	fx.Out

	Service Service `group:"toby.manager.services"`
}

type CommandService struct{}

func NewCommandService() CommandServiceResult {
	return CommandServiceResult{Service: CommandService{}}
}

func (CommandService) Commands() []Command {
	return []Command{CommandFunc{Name: control.MethodCommandExit, Run: handleCommandExit}}
}

func handleCommandExit(_ context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeCommandExitParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if runtime.Manager.CommandExit != nil {
		runtime.Manager.CommandExit(params)
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
