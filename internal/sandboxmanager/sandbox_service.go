package sandboxmanager

import (
	"context"
	"time"

	"petris.dev/toby/internal/control"

	"go.uber.org/fx"
)

type SandboxServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.manager.services"`
}

type SandboxService struct{}

func NewSandboxService() SandboxServiceResult {
	return SandboxServiceResult{Service: SandboxService{}}
}

func (SandboxService) Commands() []Command {
	return []Command{CommandFunc{Name: control.MethodSandboxTerminate, Run: handleSandboxTerminate}}
}

func handleSandboxTerminate(_ context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
	runtime.stopCommands()
	time.AfterFunc(20*time.Millisecond, func() { runtime.signalTerminate() })
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
