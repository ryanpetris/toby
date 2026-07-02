package client

// The client's approval handler must dispatch approval.prompt to the active
// foreground prompter and return its decision; with no prompter registered it denies.

import (
	"context"
	"encoding/json"
	"testing"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
	sandboxapi "petris.dev/toby/sandbox"
)

type fakePrompter struct {
	allow bool
	got   sandboxapi.ApprovalRequest
}

func (f *fakePrompter) PromptApproval(_ context.Context, req sandboxapi.ApprovalRequest) (bool, error) {
	f.got = req
	return f.allow, nil
}

func decodeAllow(t *testing.T, resp []byte) bool {
	t.Helper()
	parsed, err := control.DecodeResponse(resp)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, _ := json.Marshal(parsed.Result)
	var result protocol.ApprovalPromptResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return result.Allow
}

func promptRequest(t *testing.T) []byte {
	t.Helper()
	params, _ := json.Marshal(protocol.ApprovalPromptParams{Action: "git.push", Name: "Git push", Message: "push"})
	req, err := control.NewRequest(1, protocol.MethodApprovalPrompt, params)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestApprovalHandlerInvokesActivePrompter(t *testing.T) {
	holder := newPrompter()
	fake := &fakePrompter{allow: true}
	holder.Set(fake)

	resp, err := approvalHandler(holder)(context.Background(), promptRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if !decodeAllow(t, resp) {
		t.Fatal("expected allow=true")
	}
	if fake.got.Action != "git.push" {
		t.Fatalf("prompter got action %q", fake.got.Action)
	}
}

func TestApprovalHandlerDeniesWithoutPrompter(t *testing.T) {
	holder := newPrompter() // nothing registered
	resp, err := approvalHandler(holder)(context.Background(), promptRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if decodeAllow(t, resp) {
		t.Fatal("expected deny when no prompter is registered")
	}
}
