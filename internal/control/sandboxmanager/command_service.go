package sandboxmanager

import (
	"context"
	"syscall"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type CommandServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.manager.services"`
}

type CommandService struct{}

func NewCommandService() CommandServiceResult {
	return CommandServiceResult{Service: CommandService{}}
}

func (CommandService) Commands() []Command {
	return []Command{CommandFunc{Name: control.MethodCommandRun, Run: handleCommandRun}}
}

func handleCommandRun(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	params, err := control.DecodeCommandRunParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if err := runtime.commandRun(ctx, params); err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
