package gitservice

// Validates encoded reverse Git calls, logical errors, and authority
// disconnection.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
)

type fakeReverseCaller struct {
	call func(context.Context, json.RawMessage) (json.RawMessage, error)
}

var _ ReverseCaller = (*fakeReverseCaller)(nil)

func (c *fakeReverseCaller) Call(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	return c.call(ctx, request)
}

func TestReverseGitClientSendsEncodedHostActionRequest(t *testing.T) {
	var request hostaction.RPCRequest
	caller := &fakeReverseCaller{
		call: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var err error
			request, err = hostaction.DecodeRequest(raw)
			if err != nil {
				t.Fatal(err)
			}

			return hostaction.ResponseOK(request.ID, git.Result{
				Repository: "project",
				Stdout:     "updated",
			}), nil
		},
	}
	client := NewReverseGitClient(caller)

	result, err := client.Fetch(t.Context(), git.RepositoryParams{Repository: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != git.MethodFetch {
		t.Fatalf("request method = %q, want %q", request.Method, git.MethodFetch)
	}
	params, err := git.DecodeRepositoryParams(request.Params)
	if err != nil {
		t.Fatal(err)
	}
	if params.Repository != "project" {
		t.Fatalf("request repository = %q, want project", params.Repository)
	}
	if result.Repository != "project" || result.Stdout != "updated" {
		t.Fatalf("result = %#v", result)
	}
}

func TestReverseGitClientPreservesLogicalError(t *testing.T) {
	caller := &fakeReverseCaller{
		call: func(_ context.Context, request json.RawMessage) (json.RawMessage, error) {
			decoded, err := hostaction.DecodeRequest(request)
			if err != nil {
				t.Fatal(err)
			}

			return hostaction.ResponseError(
				decoded.ID,
				hostaction.CodePermissionDenied,
				"permission denied",
				nil,
			), io.ErrClosedPipe
		},
	}
	client := NewReverseGitClient(caller)

	_, err := client.Fetch(t.Context(), git.RepositoryParams{Repository: "project"})
	var rpcErr *hostaction.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("Fetch() error = %v, want logical RPC error", err)
	}
	if rpcErr.Code != hostaction.CodePermissionDenied {
		t.Fatalf("RPC error code = %d, want %d", rpcErr.Code, hostaction.CodePermissionDenied)
	}
	if errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("logical RPC error unexpectedly includes transport error: %v", err)
	}
}

func TestReverseGitClientFailsClosedAfterDisconnect(t *testing.T) {
	caller := &fakeReverseCaller{
		call: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, io.ErrClosedPipe
		},
	}
	client := NewReverseGitClient(caller)

	if _, err := client.Fetch(t.Context(), git.RepositoryParams{Repository: "project"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Fetch() error = %v, want closed pipe", err)
	}
}

func TestReverseGitClientRejectsMissingCaller(t *testing.T) {
	var typedNil *fakeReverseCaller
	tests := []struct {
		name   string
		caller ReverseCaller
	}{
		{name: "nil interface"},
		{name: "typed nil", caller: typedNil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewReverseGitClient(tt.caller)

			if _, err := client.Fetch(t.Context(), git.RepositoryParams{Repository: "project"}); err == nil {
				t.Fatal("Fetch succeeded without a reverse caller")
			}
		})
	}
}
