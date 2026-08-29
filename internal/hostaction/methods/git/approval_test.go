package git

// Verifies the approval error envelope: held requests report the approval id
// and denials map to permission errors.

import (
	"context"
	"testing"

	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/hostaction"
)

type fakeApprover struct {
	err error
	req approval.Request
}

func (f *fakeApprover) Authorize(_ context.Context, req approval.Request) error {
	f.req = req
	return f.err
}

func handleGitWithApprover(
	t *testing.T,
	approver Approver,
	request []byte,
) hostaction.RPCResponse {
	t.Helper()

	service := New(nil)
	service.SetApprover(approver)
	router, err := hostaction.NewRouter([]hostaction.Capability{service})
	if err != nil {
		t.Fatal(err)
	}
	req, err := hostaction.DecodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := router.Handle(context.Background(), req)
	response, err := hostaction.DecodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGitPushReportsHeldApproval(t *testing.T) {
	approver := &fakeApprover{err: &approval.PendingError{
		ID:      "a1b2c3d4e5f6",
		Name:    "Git push",
		Message: "Push main to origin in repo",
	}}
	request, err := NewPushRequest(1, "repo", "main", "", false)
	if err != nil {
		t.Fatal(err)
	}

	response := handleGitWithApprover(t, approver, request)
	if response.Error == nil ||
		response.Error.Code != hostaction.CodeApprovalRequired {
		t.Fatalf("push response error = %v", response.Error)
	}
	data, err := hostaction.DecodeApprovalRequiredData(response.Error.Data)
	if err != nil {
		t.Fatal(err)
	}
	if data.ApprovalID != "a1b2c3d4e5f6" || data.Name != "Git push" {
		t.Fatalf("approval data = %+v", data)
	}

	if approver.req.Action != MethodPush ||
		len(approver.req.Params) == 0 ||
		len(approver.req.RequestID) == 0 {
		t.Fatalf("authorize request = %+v", approver.req)
	}
}

func TestGitPushMapsDenialToPermissionError(t *testing.T) {
	approver := &fakeApprover{err: approval.ErrDenied}
	request, err := NewPushRequest(1, "repo", "main", "", false)
	if err != nil {
		t.Fatal(err)
	}

	response := handleGitWithApprover(t, approver, request)
	if response.Error == nil ||
		response.Error.Code != hostaction.CodePermissionDenied {
		t.Fatalf("denied response error = %v", response.Error)
	}
}
