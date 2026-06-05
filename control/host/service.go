// Package host is the host-side control endpoint. It accepts the sandbox's control
// connection, dispatches method capabilities through a control.Router, and handles
// the session-lifecycle methods (context.init, command.exit) inline since those
// need the per-connection client and the session's orchestration callbacks.
package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"syscall"

	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/control/methods/command"
	"petris.dev/toby/control/methods/lifecycle"
)

// Service is the host control endpoint. The orchestration callbacks are installed
// by the session at runtime; the method capabilities are dispatched via the router.
type Service struct {
	router *control.Router

	ContextInit  func(context.Context, *SandboxClient) error
	SandboxReady func(*SandboxClient, error)
	SandboxDone  func(error)
	CommandExit  func(command.ExitParams)
	HTTPProxy    *httpproxy.Service
}

func NewService(capabilities []control.Capability, httpProxy *httpproxy.Service) (*Service, error) {
	router, err := control.NewRouter(capabilities)
	if err != nil {
		return nil, err
	}
	return &Service{router: router, HTTPProxy: httpProxy}, nil
}

func (m *Service) HandleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	req, err := control.DecodeRequest(bytes.TrimSpace(request))
	if err != nil || req.Method != lifecycle.MethodContextInit {
		response, err := m.Handle(ctx, request)
		if len(response) == 0 && err != nil {
			response = control.ResponseError(nil, control.CodeInternalError, err.Error(), nil)
		}
		_, _ = conn.Write(response)
		return
	}
	m.handleSandboxConnection(ctx, conn, reader, req)
}

func (m *Service) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		var syntaxErr *json.SyntaxError
		code := control.CodeInvalidRequest
		if errors.As(err, &syntaxErr) {
			code = control.CodeParseError
		}
		return control.ResponseError(nil, code, err.Error(), nil), syscall.EINVAL
	}
	if req.Method == command.MethodExit {
		return m.handleCommandExit(req)
	}
	return m.router.Handle(ctx, req)
}

func (m *Service) handleSandboxConnection(ctx context.Context, conn net.Conn, reader *bufio.Reader, req control.RPCRequest) {
	peer := control.NewPeer(ctx, conn, m.Handle)
	peer.Start(reader)
	client := &SandboxClient{peer: peer}
	response, err := m.handleContextInit(ctx, client, req)
	if len(response) == 0 && err != nil {
		response = control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil)
	}
	_ = peer.Respond(response)
	<-peer.Done()
	if m.SandboxDone != nil {
		m.SandboxDone(peer.Err())
	}
}

// handleContextInit runs the session's context-init callback for the newly
// connected sandbox and reports readiness.
func (m *Service) handleContextInit(ctx context.Context, client *SandboxClient, req control.RPCRequest) ([]byte, error) {
	var err error
	if m.ContextInit == nil {
		err = syscall.ENOSYS
	} else {
		err = m.ContextInit(ctx, client)
	}
	if m.SandboxReady != nil {
		m.SandboxReady(client, err)
	}
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), err
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

// handleCommandExit forwards a sandbox command's completion to the session.
func (m *Service) handleCommandExit(req control.RPCRequest) ([]byte, error) {
	params, err := command.DecodeExitParams(req.Params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	if m.CommandExit != nil {
		m.CommandExit(params)
	}
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}
