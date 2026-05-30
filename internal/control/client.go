package control

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"sync/atomic"
)

type Client struct {
	Endpoint Endpoint
	next     atomic.Int64
}

func NewEndpointClient(endpoint Endpoint) *Client {
	return &Client{Endpoint: endpoint}
}

func (c *Client) GitCommit(repository, message string, amend bool) (GitResult, error) {
	request, err := NewGitCommitRequest(c.next.Add(1), repository, message, amend)
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

func (c *Client) GitPush(repository, branch, origin string, tags bool) (GitResult, error) {
	request, err := NewGitPushRequest(c.next.Add(1), repository, branch, origin, tags)
	if err != nil {
		return GitResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return GitResult{}, err
	}
	return DecodeGitResult(resp.Result)
}

func (c *Client) GitRebase(repository, base string, continueRebase, abort bool) (GitResult, error) {
	request, err := NewGitRebaseRequest(c.next.Add(1), repository, base, continueRebase, abort)
	if err != nil {
		return GitResult{}, err
	}
	resp, err := c.call(request)
	if err != nil {
		return GitResult{}, err
	}
	return DecodeGitResult(resp.Result)
}

func (c *Client) GitTag(repository, tag, message, target string) (GitResult, error) {
	request, err := NewGitTagRequest(c.next.Add(1), repository, tag, message, target)
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
	conn, err := DialEndpoint(c.Endpoint)
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
