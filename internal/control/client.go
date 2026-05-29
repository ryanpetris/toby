package control

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"petris.dev/toby/internal/config"
)

type Client struct {
	Path string
	next atomic.Int64
}

func NewClient(path string) *Client {
	return &Client{Path: path}
}

func DefaultControlPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	} else {
		stateHome = config.ExpandHome(stateHome, home)
	}
	return filepath.Join(stateHome, "toby", "control"), nil
}

func (c *Client) ProjectList() (ProjectListResult, error) {
	request, err := NewProjectListRequest(c.next.Add(1))
	if err != nil {
		return ProjectListResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return ProjectListResult{}, err
	}
	return DecodeProjectListResult(resp.Result)
}

func (c *Client) ProjectReadme(name string) (ProjectReadmeResult, error) {
	request, err := NewProjectReadmeRequest(c.next.Add(1), name)
	if err != nil {
		return ProjectReadmeResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return ProjectReadmeResult{}, err
	}
	return DecodeProjectReadmeResult(resp.Result)
}

func (c *Client) ProjectMount(name string) (MountResult, error) {
	request, err := NewProjectMountRequest(c.next.Add(1), name)
	if err != nil {
		return MountResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return MountResult{}, err
	}
	return DecodeMountResult(resp.Result)
}

func (c *Client) GitCommit(repository, message string) (GitResult, error) {
	request, err := NewGitCommitRequest(c.next.Add(1), repository, message)
	if err != nil {
		return GitResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return GitResult{}, err
	}
	return DecodeGitResult(resp.Result)
}

func (c *Client) GitFetch(repository string) (GitResult, error) {
	request, err := NewGitFetchRequest(c.next.Add(1), repository)
	if err != nil {
		return GitResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return GitResult{}, err
	}
	return DecodeGitResult(resp.Result)
}

func (c *Client) GitPush(repository, branch, origin string) (GitResult, error) {
	request, err := NewGitPushRequest(c.next.Add(1), repository, branch, origin)
	if err != nil {
		return GitResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return GitResult{}, err
	}
	return DecodeGitResult(resp.Result)
}

func (c *Client) call(request []byte) (RPCResponse, error) {
	request = append(request, '\n')
	response, writeErr := c.roundTrip(request)
	if len(bytes.TrimSpace(response)) == 0 {
		if writeErr != nil {
			return RPCResponse{}, writeErr
		}
		return RPCResponse{}, io.ErrUnexpectedEOF
	}
	resp, err := DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		if writeErr != nil {
			return RPCResponse{}, fmt.Errorf("%w; decode response: %v", writeErr, err)
		}
		return RPCResponse{}, err
	}
	if resp.Error != nil {
		return RPCResponse{}, resp.Error
	}
	if writeErr != nil {
		return RPCResponse{}, writeErr
	}
	return resp, nil
}

func (c *Client) roundTrip(request []byte) ([]byte, error) {
	file, err := os.OpenFile(c.Path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	_, writeErr := file.Write(request)
	if _, err := file.Seek(0, io.SeekStart); err != nil && writeErr == nil {
		writeErr = err
	}
	response, readErr := io.ReadAll(file)
	if readErr != nil && writeErr == nil {
		writeErr = readErr
	}
	return response, writeErr
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
