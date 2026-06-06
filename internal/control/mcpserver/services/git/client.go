package gitservice

// The default GitClient: forwards each Git tool call to the host Git capability
// as an encoded request, decodes the response, and surfaces the host Git result
// (so commits are signed and use the host's credentials).

import (
	"context"
	"fmt"
	"io"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/control/methods/git"
)

func NewHostGitClient(service *host.Service) mcpserver.GitClient {
	return &hostGitClient{service: service}
}

type hostGitClient struct {
	service *host.Service
}

var _ mcpserver.GitClient = (*hostGitClient)(nil)

func (c *hostGitClient) Commit(ctx context.Context, input git.CommitParams) (git.Result, error) {
	request, err := git.NewCommitRequest(1, input.Repository, input.Message, input.Amend)
	return c.call(ctx, request, err)
}

func (c *hostGitClient) Fetch(ctx context.Context, input git.RepositoryParams) (git.Result, error) {
	request, err := git.NewFetchRequest(1, input.Repository)
	return c.call(ctx, request, err)
}

func (c *hostGitClient) Push(ctx context.Context, input git.PushParams) (git.Result, error) {
	request, err := git.NewPushRequest(1, input.Repository, input.Branch, input.Origin, input.Tags)
	return c.call(ctx, request, err)
}

func (c *hostGitClient) Rebase(ctx context.Context, input git.RebaseParams) (git.Result, error) {
	request, err := git.NewRebaseRequest(1, input.Repository, input.Base, input.Continue, input.Abort)
	return c.call(ctx, request, err)
}

func (c *hostGitClient) Tag(ctx context.Context, input git.TagParams) (git.Result, error) {
	request, err := git.NewTagRequest(1, input.Repository, input.Tag, input.Message, input.Target)
	return c.call(ctx, request, err)
}

func (c *hostGitClient) call(ctx context.Context, request []byte, err error) (git.Result, error) {
	if err != nil {
		return git.Result{}, err
	}
	if c == nil || c.service == nil {
		return git.Result{}, fmt.Errorf("host service is not configured")
	}
	response, err := c.service.Handle(ctx, request)
	if len(response) == 0 {
		if err != nil {
			return git.Result{}, err
		}
		return git.Result{}, io.ErrUnexpectedEOF
	}
	decoded, decodeErr := control.DecodeResponse(response)
	if decodeErr != nil {
		if err != nil {
			return git.Result{}, fmt.Errorf("%w; decode response: %v", err, decodeErr)
		}
		return git.Result{}, decodeErr
	}
	if decoded.Error != nil {
		return git.Result{}, decoded.Error
	}
	if err != nil {
		return git.Result{}, err
	}
	return git.DecodeResult(decoded.Result)
}
