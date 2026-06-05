package host

// The SandboxClient: the host's outbound peer to the connected sandbox. The
// session uses it to push files, environment, and commands into the sandbox and
// to resolve host-identity sentinels to the host's real uid/gid.

import (
	"context"
	"errors"
	"os"

	"petris.dev/toby/control"
	"petris.dev/toby/control/methods/command"
	"petris.dev/toby/control/methods/env"
	"petris.dev/toby/control/methods/files"
	"petris.dev/toby/control/methods/lifecycle"
)

// SandboxClient is the host's outbound channel to one connected sandbox.
type SandboxClient struct {
	peer *control.Peer
}

func (c *SandboxClient) FileCreate(ctx context.Context, path string, data []byte, mode uint32) error {
	_, err := c.peer.Call(ctx, files.MethodCreate, files.CreateParams{Path: path, Data: data, Mode: mode})
	return err
}

func (c *SandboxClient) FileCreateOwned(ctx context.Context, path string, data []byte, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, files.MethodCreate, files.CreateParams{Path: path, Data: data, Mode: mode, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) FileDelete(ctx context.Context, path string, recursive bool) error {
	_, err := c.peer.Call(ctx, files.MethodDelete, files.DeleteParams{Path: path, Recursive: recursive})
	return err
}

func (c *SandboxClient) FileMkdir(ctx context.Context, path string, mode uint32) error {
	_, err := c.peer.Call(ctx, files.MethodMkdir, files.MkdirParams{Path: path, Mode: mode})
	return err
}

func (c *SandboxClient) FileMkdirOwned(ctx context.Context, path string, mode uint32, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, files.MethodMkdir, files.MkdirParams{Path: path, Mode: mode, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) FileSymlink(ctx context.Context, path, target string) error {
	_, err := c.peer.Call(ctx, files.MethodSymlink, files.SymlinkParams{Path: path, Target: target})
	return err
}

func (c *SandboxClient) FileSymlinkOwned(ctx context.Context, path, target string, uid, gid int) error {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, files.MethodSymlink, files.SymlinkParams{Path: path, Target: target, UID: uid, GID: gid})
	return err
}

func (c *SandboxClient) EnvironmentGet(ctx context.Context) (map[string]string, error) {
	resp, err := c.peer.Call(ctx, env.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	result, err := env.DecodeGetResult(resp.Result)
	if err != nil {
		return nil, err
	}
	return result.Environment, nil
}

func (c *SandboxClient) EnvironmentSet(ctx context.Context, name, value string) error {
	_, err := c.peer.Call(ctx, env.MethodSet, env.SetParams{Name: name, Value: value})
	return err
}

func (c *SandboxClient) CommandRun(ctx context.Context, params command.RunParams) error {
	var err error
	params, err = resolveCommandRunParams(params)
	if err != nil {
		return err
	}
	_, err = c.peer.Call(ctx, command.MethodRun, params)
	return err
}

func (c *SandboxClient) Terminate(ctx context.Context) error {
	_, err := c.peer.Call(ctx, lifecycle.MethodSandboxTerminate, nil)
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

func resolveCommandRunParams(params command.RunParams) (command.RunParams, error) {
	useHostGroups := params.UID == control.HostUser || params.GID == control.HostGroup
	uid, err := resolveUID(params.UID)
	if err != nil {
		return command.RunParams{}, err
	}
	gid, err := resolveGID(params.GID)
	if err != nil {
		return command.RunParams{}, err
	}
	groups := params.Groups
	if len(groups) == 0 && useHostGroups {
		groups, err = os.Getgroups()
		if err != nil {
			return command.RunParams{}, err
		}
	} else {
		groups, err = resolveGroups(groups)
		if err != nil {
			return command.RunParams{}, err
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
