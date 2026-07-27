package gitservice

// Implements the Git MCP client over a live launch-owned reverse capability.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/tobymcp"
)

// ReverseCaller sends one encoded host-action request over a live launch
// capability and returns its encoded response.
type ReverseCaller interface {
	// Call sends one encoded host-action request.
	Call(context.Context, json.RawMessage) (json.RawMessage, error)
}

// NewReverseGitClient creates a Git client whose authority lasts only as long
// as caller continues accepting reverse capability calls.
func NewReverseGitClient(caller ReverseCaller) tobymcp.GitClient {
	return &reverseGitClient{caller: caller}
}

type reverseGitClient struct {
	caller ReverseCaller
}

var _ tobymcp.GitClient = (*reverseGitClient)(nil)

func (c *reverseGitClient) Commit(ctx context.Context, input git.CommitParams) (git.Result, error) {
	request, err := git.NewCommitRequest(1, input.Repository, input.Message, input.Amend)
	return c.call(ctx, request, err)
}

func (c *reverseGitClient) Fetch(ctx context.Context, input git.RepositoryParams) (git.Result, error) {
	request, err := git.NewFetchRequest(1, input.Repository)
	return c.call(ctx, request, err)
}

func (c *reverseGitClient) Push(ctx context.Context, input git.PushParams) (git.Result, error) {
	request, err := git.NewPushRequest(1, input.Repository, input.Branch, input.Origin, input.Tags)
	return c.call(ctx, request, err)
}

func (c *reverseGitClient) Rebase(ctx context.Context, input git.RebaseParams) (git.Result, error) {
	request, err := git.NewRebaseRequest(1, input.Repository, input.Base, input.Continue, input.Abort)
	return c.call(ctx, request, err)
}

func (c *reverseGitClient) Tag(ctx context.Context, input git.TagParams) (git.Result, error) {
	request, err := git.NewTagRequest(1, input.Repository, input.Tag, input.Message, input.Target)
	return c.call(ctx, request, err)
}

func (c *reverseGitClient) call(ctx context.Context, request []byte, buildErr error) (git.Result, error) {
	if buildErr != nil {
		return git.Result{}, buildErr
	}
	if c == nil || !hasCaller(c.caller) {
		return git.Result{}, fmt.Errorf("reverse Git caller is not configured")
	}

	response, err := c.caller.Call(ctx, json.RawMessage(request))
	return decodeResponse(response, err)
}

func hasCaller(caller ReverseCaller) bool {
	if caller == nil {
		return false
	}

	value := reflect.ValueOf(caller)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}
