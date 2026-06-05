package sandbox

// The per-launch in-sandbox control endpoint: Runtime dials the host, drives the
// connection lifecycle, dispatches inbound requests to the capability router, and
// handles sandbox.terminate inline (it owns the terminate signal). Command and
// environment state live in their capability services.

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/control"
	commandcap "petris.dev/toby/control/methods/command"
	envcap "petris.dev/toby/control/methods/env"
	"petris.dev/toby/control/methods/lifecycle"
)

type Runtime struct {
	peer    *control.Peer
	router  *control.Router
	env     *envcap.Service
	command *commandcap.Service

	terminate chan struct{}
	once      sync.Once
}

func NewRuntime(router *control.Router, environment *envcap.Service, commands *commandcap.Service) *Runtime {
	if environment == nil {
		environment = envcap.New()
	}
	if commands == nil {
		commands = commandcap.New(environment)
	}
	return &Runtime{router: router, env: environment, command: commands, terminate: make(chan struct{})}
}

func (r *Runtime) Run(ctx context.Context, controlPath string) error {
	endpoint, err := control.DefaultEndpoint()
	if err != nil {
		return err
	}
	conn, err := control.DialEndpoint(ctx, endpoint)
	if err != nil {
		return err
	}
	peer := control.NewPeer(ctx, conn, r.Handle)
	r.peer = peer
	r.command.SetSender(peer)
	peer.Start(nil)
	stopSignals := r.forwardSignals()
	defer stopSignals()
	if _, err := peer.Call(ctx, lifecycle.MethodContextInit, nil); err != nil {
		_ = peer.Close()
		return err
	}
	select {
	case <-r.terminate:
		_ = peer.Close()
		return nil
	case <-peer.Done():
		if err := peer.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		r.command.StopAll()
		_ = peer.Close()
		return ctx.Err()
	}
}

func (r *Runtime) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		return control.ResponseError(nil, control.CodeInvalidRequest, err.Error(), nil), syscall.EINVAL
	}
	if req.Method == lifecycle.MethodSandboxTerminate {
		return r.handleTerminate(req)
	}
	return r.router.Handle(ctx, req)
}

// handleTerminate stops running commands and schedules a graceful shutdown.
func (r *Runtime) handleTerminate(req control.RPCRequest) ([]byte, error) {
	r.stopCommands()
	time.AfterFunc(20*time.Millisecond, func() { r.signalTerminate() })
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func (r *Runtime) stopCommands() { r.command.StopAll() }

func (r *Runtime) signalTerminate() {
	r.once.Do(func() { close(r.terminate) })
}

func (r *Runtime) forwardSignals() func() {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sig := range signals {
			r.command.SignalForeground(sig)
		}
	}()
	return func() {
		signal.Stop(signals)
		close(signals)
		<-done
	}
}
