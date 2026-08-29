package approval

// Exercises the approval record state machine: dedup, single execution,
// one-shot grants, saved responses, waits, and teardown denial.

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testRequest(action string, params string) Request {
	return Request{
		Action:    action,
		Name:      "Test action",
		Message:   "Test message",
		RequestID: json.RawMessage("1"),
		Params:    json.RawMessage(params),
	}
}

func TestRegistryCreateReusesPendingRecord(t *testing.T) {
	registry := NewRegistry()

	first, err := registry.Create(testRequest("git.push", `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	same, err := registry.Create(testRequest("git.push", `{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != first.ID {
		t.Fatalf("dedup id = %s, want %s", same.ID, first.ID)
	}

	other, err := registry.Create(testRequest("git.push", `{"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("distinct params reused the same record")
	}
	if len(first.ID) != approvalIDBytes*2 {
		t.Fatalf("approval id %q has unexpected length", first.ID)
	}

	pending := registry.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending records = %d, want 2", len(pending))
	}
}

func TestRegistryApproveExecutesExactlyOnce(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	_, status, changed, err := registry.Approve(created.ID)
	if err != nil || !changed || status != StatusExecuting {
		t.Fatalf("first approve = (%v, %v, %v)", status, changed, err)
	}
	_, status, changed, err = registry.Approve(created.ID)
	if err != nil || changed || status != StatusExecuting {
		t.Fatalf("second approve = (%v, %v, %v)", status, changed, err)
	}
	if _, _, changed, _ := registry.Deny(created.ID); changed {
		t.Fatal("deny changed an executing record")
	}
	if pending := registry.Pending(); len(pending) != 0 {
		t.Fatalf("executing record still listed as pending: %v", pending)
	}
}

func TestRegistryDenyIsFinal(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	_, status, changed, err := registry.Deny(created.ID)
	if err != nil || !changed || status != StatusDenied {
		t.Fatalf("deny = (%v, %v, %v)", status, changed, err)
	}
	_, status, changed, err = registry.Approve(created.ID)
	if err != nil || changed || status != StatusDenied {
		t.Fatalf("approve after deny = (%v, %v, %v)", status, changed, err)
	}

	if _, _, _, err := registry.Approve("unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown approve error = %v, want %v", err, ErrNotFound)
	}
}

func TestRegistryRedeemGrantIsOneShotAndActionBound(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	if err := registry.RedeemGrant(created.ID, "git.push"); !errors.Is(err, ErrDenied) {
		t.Fatalf("pending redeem error = %v, want %v", err, ErrDenied)
	}
	if _, _, _, err := registry.Approve(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.RedeemGrant(created.ID, "git.tag"); !errors.Is(err, ErrDenied) {
		t.Fatalf("action mismatch error = %v, want %v", err, ErrDenied)
	}
	if err := registry.RedeemGrant(created.ID, "git.push"); err != nil {
		t.Fatalf("redeem error = %v", err)
	}
	if err := registry.RedeemGrant(created.ID, "git.push"); !errors.Is(err, ErrDenied) {
		t.Fatalf("second redeem error = %v, want %v", err, ErrDenied)
	}
	if err := registry.RedeemGrant("unknown", "git.push"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown redeem error = %v, want %v", err, ErrNotFound)
	}
}

func TestRegistryWaitDeliversSavedResponseRepeatedly(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	type waitResult struct {
		status   Status
		response []byte
		err      error
	}
	results := make(chan waitResult, 1)
	go func() {
		_, status, response, waitErr := registry.Wait(
			t.Context(),
			created.ID,
			time.Minute,
		)
		results <- waitResult{status, response, waitErr}
	}()

	if _, _, _, err := registry.Approve(created.ID); err != nil {
		t.Fatal(err)
	}
	saved := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	if err := registry.SaveResponse(created.ID, saved); err != nil {
		t.Fatal(err)
	}

	first := <-results
	if first.err != nil ||
		first.status != StatusCompleted ||
		string(first.response) != string(saved) {
		t.Fatalf("blocked wait = %+v", first)
	}

	record, status, response, err := registry.Wait(t.Context(), created.ID, time.Minute)
	if err != nil || status != StatusCompleted || string(response) != string(saved) {
		t.Fatalf("repeat wait = (%v, %q, %v)", status, response, err)
	}
	if record.Action != "git.push" {
		t.Fatalf("wait record action = %q", record.Action)
	}
}

func TestRegistryWaitTimesOutWhilePending(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	_, status, response, err := registry.Wait(
		t.Context(),
		created.ID,
		time.Millisecond,
	)
	if err != nil || status != StatusPending || response != nil {
		t.Fatalf("timed-out wait = (%v, %q, %v)", status, response, err)
	}
	if _, _, _, err := registry.Wait(
		t.Context(),
		"unknown",
		time.Millisecond,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown wait error = %v, want %v", err, ErrNotFound)
	}
}

func TestRegistryDenyAllWakesWaiters(t *testing.T) {
	registry := NewRegistry()
	created, err := registry.Create(testRequest("git.push", `{}`))
	if err != nil {
		t.Fatal(err)
	}

	statuses := make(chan Status, 2)
	for range 2 {
		go func() {
			_, status, _, _ := registry.Wait(
				t.Context(),
				created.ID,
				time.Minute,
			)
			statuses <- status
		}()
	}

	// Both waiters must be attached before the sweep for the test to verify
	// the broadcast; a short settle keeps this simple and race-free because a
	// late waiter still observes the denied status immediately.
	time.Sleep(10 * time.Millisecond)
	registry.DenyAll()
	for range 2 {
		if status := <-statuses; status != StatusDenied {
			t.Fatalf("waiter status = %v, want %v", status, StatusDenied)
		}
	}
}

func TestRegistryCreateEnforcesRecordCap(t *testing.T) {
	registry := NewRegistry()
	for index := range maxLiveRecords {
		if _, err := registry.Create(testRequest(
			"git.push",
			`{"index":`+string(rune('a'+index%26))+`"`+string(rune('a'+index/26))+`"}`,
		)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := registry.Create(testRequest("git.push", `{"overflow":true}`)); err == nil {
		t.Fatal("record cap was not enforced")
	}
}
