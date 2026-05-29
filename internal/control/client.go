package control

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"petris.dev/toby/internal/config"
)

const SandboxSocketName = "sandbox.sock"

type Client struct {
	Path string
	next atomic.Int64
}

func NewClient(path string) *Client {
	return &Client{Path: path}
}

func DefaultSocketPath() (string, error) {
	runtimeDir, err := defaultRuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(runtimeDir, "toby", SandboxSocketName), nil
}

func HostSocketPath(runtimeDir string, pid int) string {
	return filepath.Join(runtimeDir, "toby", "control", fmt.Sprintf("%d.sock", pid))
}

func defaultRuntimeDir() (string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return config.ExpandHome(runtimeDir, home), nil
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
	conn, err := net.Dial("unix", c.Path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_, writeErr := conn.Write(request)
	response, readErr := bufio.NewReader(conn).ReadBytes('\n')
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
