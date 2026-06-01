package hostmanager

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"syscall"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"

	"go.uber.org/fx"
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
	HTTPProxy          *httpproxy.Service
}

type HostManagerParams struct {
	fx.In

	Registry  *Registry
	HTTPProxy *httpproxy.Service `optional:"true"`
}

type Runtime struct {
	Manager *HostManager
	Sandbox *SandboxClient
}

type SandboxClient struct {
	peer *control.Peer
}

func NewHostManager(params HostManagerParams) *HostManager {
	return &HostManager{Registry: params.Registry, HTTPProxy: params.HTTPProxy}
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

func (c *SandboxClient) FileCreateOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, control.MethodFileCreate, control.FileCreateParams{Path: path, Data: data, Mode: mode, UID: uid, GID: gid})
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

func (c *SandboxClient) FileMkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, control.MethodFileMkdir, control.FileMkdirParams{Path: path, Mode: mode, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) FileSymlink(ctx context.Context, path, target string) error {
	_, err := c.peer.Call(ctx, control.MethodFileSymlink, control.FileSymlinkParams{Path: path, Target: target})
	return err
}

func (c *SandboxClient) FileSymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, control.MethodFileSymlink, control.FileSymlinkParams{Path: path, Target: target, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) EnvironmentGet(ctx context.Context) (map[string]string, error) {
	resp, err := c.peer.Call(ctx, control.MethodEnvironmentGet, nil)
	if err != nil {
		return nil, err
	}
	result, err := control.DecodeEnvironmentGetResult(resp.Result)
	if err != nil {
		return nil, err
	}
	return result.Environment, nil
}

func (c *SandboxClient) EnvironmentSet(ctx context.Context, name, value string) error {
	_, err := c.peer.Call(ctx, control.MethodEnvironmentSet, control.EnvironmentSetParams{Name: name, Value: value})
	return err
}

func (c *SandboxClient) CommandRun(ctx context.Context, params control.CommandRunParams) error {
	var err error
	params, err = resolveCommandRunParams(params)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, control.MethodCommandRun, params)
	return err
}

func (c *SandboxClient) Terminate(ctx context.Context) error {
	_, err := c.peer.Call(ctx, control.MethodSandboxTerminate, nil)
	return err
}

func resolveOwner(uid, gid int) (int, int, error) {
	resolvedUID, err := resolveUID(uid)
	if err != nil {
		return 0, 0, err
	}
	resolvedGID, err := resolveGID(gid)
	if err != nil {
		return 0, 0, err
	}
	return resolvedUID, resolvedGID, nil
}

func resolveCommandRunParams(params control.CommandRunParams) (control.CommandRunParams, error) {
	useHostGroups := params.UID == control.HostUser || params.GID == control.HostGroup
	uid, err := resolveUID(params.UID)
	if err != nil {
		return control.CommandRunParams{}, err
	}
	gid, err := resolveGID(params.GID)
	if err != nil {
		return control.CommandRunParams{}, err
	}
	groups := params.Groups
	if len(groups) == 0 && useHostGroups {
		groups, err = os.Getgroups()
		if err != nil {
			return control.CommandRunParams{}, err
		}
	} else {
		groups, err = resolveGroups(groups)
		if err != nil {
			return control.CommandRunParams{}, err
		}
	}
	params.UID = uid
	params.GID = gid
	params.Groups = append([]int(nil), groups...)
	return params, nil
}

func resolveUID(uid int) (int, error) {
	switch {
	case uid == control.HostUser:
		return os.Getuid(), nil
	case uid < 0:
		return 0, errors.New("invalid uid")
	default:
		return uid, nil
	}
}

func resolveGID(gid int) (int, error) {
	switch {
	case gid == control.HostGroup:
		return os.Getgid(), nil
	case gid < 0:
		return 0, errors.New("invalid gid")
	default:
		return gid, nil
	}
}

func resolveGroups(groups []int) ([]int, error) {
	resolved := make([]int, 0, len(groups))
	for _, group := range groups {
		switch {
		case group == control.HostGroup:
			hostGroups, err := os.Getgroups()
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, hostGroups...)
		case group < 0:
			return nil, errors.New("invalid supplementary gid")
		default:
			resolved = append(resolved, group)
		}
	}
	return resolved, nil
}
