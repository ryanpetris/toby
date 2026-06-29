package host

// SandboxClient is the host's outbound control client to one connected sandbox
// manager. It resolves host-identity sentinels before sending file requests.

import (
	"context"
	"errors"
	"os"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/methods/files"
)

type Caller interface {
	Call(context.Context, string, any) (control.RPCResponse, error)
}

type SandboxClient struct {
	caller Caller
}

func NewSandboxClient(caller Caller) *SandboxClient {
	return &SandboxClient{caller: caller}
}

func (c *SandboxClient) FileCreate(ctx context.Context, path string, data []byte, mode uint32) error {
	_, err := c.caller.Call(ctx, files.MethodCreate, files.CreateParams{Path: path, Data: data, Mode: mode})
	return err
}

func (c *SandboxClient) FileCreateOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.caller.Call(ctx, files.MethodCreate, files.CreateParams{Path: path, Data: data, Mode: mode, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) FileDelete(ctx context.Context, path string, recursive bool) error {
	_, err := c.caller.Call(ctx, files.MethodDelete, files.DeleteParams{Path: path, Recursive: recursive})
	return err
}

func (c *SandboxClient) FileMkdir(ctx context.Context, path string, mode uint32) error {
	_, err := c.caller.Call(ctx, files.MethodMkdir, files.MkdirParams{Path: path, Mode: mode})
	return err
}

func (c *SandboxClient) FileMkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.caller.Call(ctx, files.MethodMkdir, files.MkdirParams{Path: path, Mode: mode, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) FileSymlink(ctx context.Context, path, target string) error {
	_, err := c.caller.Call(ctx, files.MethodSymlink, files.SymlinkParams{Path: path, Target: target})
	return err
}

func (c *SandboxClient) FileSymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.caller.Call(ctx, files.MethodSymlink, files.SymlinkParams{Path: path, Target: target, UID: uid, GID: gid})
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
