package approvals

// Drives the approvals.* methods through a real router: listing, deciding
// with approve-time execution under a grant, waiting, and denial.

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/hostaction"
)

type approvalsFixture struct {
	registry   *approval.Registry
	service    *Service
	router     *hostaction.Router
	dispatched atomic.Int32
}

func newApprovalsFixture(t *testing.T) *approvalsFixture {
	t.Helper()

	fixture := &approvalsFixture{
		registry: approval.NewRegistry(),
		service:  New(nil),
	}
	router, err := hostaction.NewRouter(
		[]hostaction.Capability{fixture.service, heldCapability{fixture}},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.router = router
	fixture.service.Bind(fixture.registry, router.Handle)
	t.Cleanup(fixture.service.Close)

	return fixture
}

// heldCapability stands in for an approvable capability: it fails without a
// redeemable grant, mirroring the git handlers' authorize step.
type heldCapability struct {
	fixture *approvalsFixture
}

func (c heldCapability) Methods() []hostaction.Method {
	return []hostaction.Method{{
		Name: "test.echo",
		Handle: func(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
			c.fixture.dispatched.Add(1)
			return hostaction.ResponseOK(req.ID, map[string]string{
				"echo": string(req.Params),
			}), nil
		},
	}}
}

func (f *approvalsFixture) handle(t *testing.T, request []byte) hostaction.RPCResponse {
	t.Helper()

	req, err := hostaction.DecodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := f.router.Handle(t.Context(), req)
	response, err := hostaction.DecodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func (f *approvalsFixture) hold(t *testing.T) approval.Record {
	t.Helper()

	record, err := f.registry.Create(approval.Request{
		Action:    "test.echo",
		Name:      "Test echo",
		Message:   "Echo the request",
		RequestID: json.RawMessage("7"),
		Params:    json.RawMessage(`{"value":"held"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestApprovalsListReportsPendingRecords(t *testing.T) {
	fixture := newApprovalsFixture(t)
	record := fixture.hold(t)

	request, err := NewListRequest(1)
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.handle(t, request)
	if response.Error != nil {
		t.Fatalf("list error = %v", response.Error)
	}
	result, err := DecodeListResult(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Approvals) != 1 ||
		result.Approvals[0].ApprovalID != record.ID ||
		result.Approvals[0].Action != "test.echo" {
		t.Fatalf("list result = %+v", result)
	}
}

func TestApprovalsApproveExecutesHeldRequestAndWaitReturnsIt(t *testing.T) {
	fixture := newApprovalsFixture(t)
	record := fixture.hold(t)

	decideRequest, err := NewDecideRequest(1, DecideParams{
		ApprovalID: record.ID,
		Approve:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	decideResponse := fixture.handle(t, decideRequest)
	if decideResponse.Error != nil {
		t.Fatalf("decide error = %v", decideResponse.Error)
	}
	decision, err := DecodeDecideResult(decideResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != StatusApproved || !decision.Changed {
		t.Fatalf("decision = %+v", decision)
	}

	waitRequest, err := NewWaitRequest(1, WaitParams{
		ApprovalID: record.ID,
		TimeoutMS:  int64(time.Minute / time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitResponse := fixture.handle(t, waitRequest)
	if waitResponse.Error != nil {
		t.Fatalf("wait error = %v", waitResponse.Error)
	}
	result, err := DecodeWaitResult(waitResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusApproved || result.Action != "test.echo" {
		t.Fatalf("wait result = %+v", result)
	}

	held, err := hostaction.DecodeResponse(result.Response)
	if err != nil {
		t.Fatal(err)
	}
	if held.Error != nil {
		t.Fatalf("held response error = %v", held.Error)
	}
	if string(held.ID) != "7" {
		t.Fatalf("held response id = %s, want the original request id", held.ID)
	}
	if fixture.dispatched.Load() != 1 {
		t.Fatalf("held request dispatched %d times", fixture.dispatched.Load())
	}

	// A second approve does not execute again.
	repeat := fixture.handle(t, decideRequest)
	if repeat.Error != nil {
		t.Fatalf("repeat decide error = %v", repeat.Error)
	}
	repeatDecision, err := DecodeDecideResult(repeat.Result)
	if err != nil {
		t.Fatal(err)
	}
	if repeatDecision.Changed {
		t.Fatal("repeat approve reported a change")
	}
	fixture.service.Close()
	if fixture.dispatched.Load() != 1 {
		t.Fatalf("held request dispatched %d times", fixture.dispatched.Load())
	}
}

func TestApprovalsDenyWakesWaitWithDenied(t *testing.T) {
	fixture := newApprovalsFixture(t)
	record := fixture.hold(t)

	decideRequest, err := NewDecideRequest(1, DecideParams{
		ApprovalID: record.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := fixture.handle(t, decideRequest); response.Error != nil {
		t.Fatalf("deny error = %v", response.Error)
	}

	waitRequest, err := NewWaitRequest(1, WaitParams{
		ApprovalID: record.ID,
		TimeoutMS:  int64(time.Minute / time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitResponse := fixture.handle(t, waitRequest)
	result, err := DecodeWaitResult(waitResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusDenied || len(result.Response) != 0 {
		t.Fatalf("wait result = %+v", result)
	}
	if fixture.dispatched.Load() != 0 {
		t.Fatal("denied request was dispatched")
	}
}

func TestApprovalsWaitTimesOutPendingAndRejectsUnknownIDs(t *testing.T) {
	fixture := newApprovalsFixture(t)
	record := fixture.hold(t)

	waitRequest, err := NewWaitRequest(1, WaitParams{
		ApprovalID: record.ID,
		TimeoutMS:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitResponse := fixture.handle(t, waitRequest)
	result, err := DecodeWaitResult(waitResponse.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusPending {
		t.Fatalf("wait result = %+v", result)
	}

	unknownRequest, err := NewWaitRequest(1, WaitParams{ApprovalID: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := fixture.handle(t, unknownRequest)
	if unknown.Error == nil ||
		unknown.Error.Code != hostaction.CodeApprovalNotFound {
		t.Fatalf("unknown wait error = %v", unknown.Error)
	}
}
