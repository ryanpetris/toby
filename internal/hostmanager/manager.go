package hostmanager

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"syscall"

	"petris.dev/toby/internal/control"
)

type RepositoryResolver interface {
	VisibleHostPath(string) (string, error)
}

type HostManager struct {
	Registry           *Registry
	RepositoryResolver RepositoryResolver
	ContextInit        func(context.Context, *SandboxClient) error
	SandboxReady       func(*SandboxClient, error)
	SandboxDone        func(error)
	CommandExit        func(control.CommandExitParams)
}

type Runtime struct {
	Manager *HostManager
	Sandbox *SandboxClient
}

type SandboxClient struct {
	peer *control.Peer
}

func NewHostManager(registry *Registry) *HostManager {
	return &HostManager{Registry: registry}
}

func (m *HostManager) HandleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	request, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	req, err := control.DecodeRequest(bytes.TrimSpace(request))
	if err != nil || req.Method != control.MethodContextInit {
		response, err := m.Handle(ctx, request)
		if len(response) == 0 && err != nil {
			response = control.ResponseError(nil, control.CodeInternalError, err.Error(), nil)
		}
		_, _ = conn.Write(response)
		return
	}
	m.handleSandboxConnection(ctx, conn, reader, req)
}

func (m *HostManager) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		var syntaxErr *json.SyntaxError
		code := control.CodeInvalidRequest
		if errors.As(err, &syntaxErr) {
			code = control.CodeParseError
		}
		return control.ResponseError(nil, code, err.Error(), nil), syscall.EINVAL
	}
	return m.Registry.Handle(ctx, &Runtime{Manager: m}, req)
}

func (m *HostManager) handleSandboxConnection(ctx context.Context, conn net.Conn, reader *bufio.Reader, req control.RPCRequest) {
	peer := control.NewPeer(ctx, conn, m.Handle)
	peer.Start(reader)
	client := &SandboxClient{peer: peer}
	runtime := &Runtime{Manager: m, Sandbox: client}
	response, err := m.Registry.Handle(ctx, runtime, req)
	if len(response) == 0 && err != nil {
		response = control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil)
	}
	_ = peer.Respond(response)
	<-peer.Done()
	if m.SandboxDone != nil {
		m.SandboxDone(peer.Err())
	}
}

func (c *SandboxClient) FileCreate(ctx context.Context, path string, data []byte, mode uint32) error {
	_, err := c.peer.Call(ctx, control.MethodFileCreate, control.FileCreateParams{Path: path, Data: data, Mode: mode})
	return err
}

func (c *SandboxClient) FileDelete(ctx context.Context, path string, recursive bool) error {
	_, err := c.peer.Call(ctx, control.MethodFileDelete, control.FileDeleteParams{Path: path, Recursive: recursive})
	return err
}

func (c *SandboxClient) FileMkdir(ctx context.Context, path string, mode uint32) error {
	_, err := c.peer.Call(ctx, control.MethodFileMkdir, control.FileMkdirParams{Path: path, Mode: mode})
	return err
}

func (c *SandboxClient) FileSymlink(ctx context.Context, path, target string) error {
	_, err := c.peer.Call(ctx, control.MethodFileSymlink, control.FileSymlinkParams{Path: path, Target: target})
	return err
}

func (c *SandboxClient) CommandRun(ctx context.Context, params control.CommandRunParams) error {
	_, err := c.peer.Call(ctx, control.MethodCommandRun, params)
	return err
}

func (c *SandboxClient) Terminate(ctx context.Context) error {
	_, err := c.peer.Call(ctx, control.MethodSandboxTerminate, nil)
	return err
}
