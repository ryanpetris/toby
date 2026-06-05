package mcpserver

import (
	"context"
	"fmt"
	"io"

	"petris.dev/toby/control"
	"petris.dev/toby/control/host"
	"petris.dev/toby/control/methods/git"
)

func NewHostManagerGitClient(manager *host.Service) GitClient {
	return &hostManagerGitClient{manager: manager}
}

type hostManagerGitClient struct {
	manager *host.Service
}

func (c *hostManagerGitClient) GitCommit(ctx context.Context, input GitCommitInput) (GitOutput, error) {
	request, err := git.NewCommitRequest(1, input.Repository, input.Message, input.Amend)
	return c.call(ctx, request, err)
}

func (c *hostManagerGitClient) GitFetch(ctx context.Context, input GitRepositoryInput) (GitOutput, error) {
	request, err := git.NewFetchRequest(1, input.Repository)
	return c.call(ctx, request, err)
}

func (c *hostManagerGitClient) GitPush(ctx context.Context, input GitPushInput) (GitOutput, error) {
	request, err := git.NewPushRequest(1, input.Repository, input.Branch, input.Origin, input.Tags)
	return c.call(ctx, request, err)
}

func (c *hostManagerGitClient) GitRebase(ctx context.Context, input GitRebaseInput) (GitOutput, error) {
	request, err := git.NewRebaseRequest(1, input.Repository, input.Base, input.Continue, input.Abort)
	return c.call(ctx, request, err)
}

func (c *hostManagerGitClient) GitTag(ctx context.Context, input GitTagInput) (GitOutput, error) {
	request, err := git.NewTagRequest(1, input.Repository, input.Tag, input.Message, input.Target)
	return c.call(ctx, request, err)
}

func (c *hostManagerGitClient) call(ctx context.Context, request []byte, err error) (GitOutput, error) {
	if err != nil {
		return GitOutput{}, err
	}
	if c == nil || c.manager == nil {
		return GitOutput{}, fmt.Errorf("host manager is not configured")
	}
	response, err := c.manager.Handle(ctx, request)
	if len(response) == 0 {
		if err != nil {
			return GitOutput{}, err
		}
		return GitOutput{}, io.ErrUnexpectedEOF
	}
	decoded, decodeErr := control.DecodeResponse(response)
	if decodeErr != nil {
		if err != nil {
			return GitOutput{}, fmt.Errorf("%w; decode response: %v", err, decodeErr)
		}
		return GitOutput{}, decodeErr
	}
	if decoded.Error != nil {
		return GitOutput{}, decoded.Error
	}
	if err != nil {
		return GitOutput{}, err
	}
	result, err := git.DecodeResult(decoded.Result)
	return GitOutput(result), err
}
