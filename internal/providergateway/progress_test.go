package providergateway

// Verifies provider progress fanout without merging concurrent operations or
// forwarding terminal events to acquisitions that missed the matching start.

import (
	"testing"

	"petris.dev/toby/internal/agent/protocol"
)

type progressRecorder struct {
	events []protocol.AcquireProgress
}

func (r *progressRecorder) Report(event protocol.AcquireProgress) error {
	r.events = append(r.events, event)
	return nil
}

func TestProgressFanoutKeepsSameSourceOperationsDistinct(t *testing.T) {
	t.Parallel()

	service := &Gateway{}
	recorder := &progressRecorder{}
	service.registerProgress(1, recorder)

	events := []protocol.AcquireProgress{
		{
			Operation: "first",
			Kind:      protocol.ProgressStep,
			Source:    "caddy",
			Text:      "First",
		},
		{
			Operation: "second",
			Kind:      protocol.ProgressStep,
			Source:    "caddy",
			Text:      "Second",
		},
		{
			Operation: "first",
			Kind:      protocol.ProgressOutput,
			Source:    "caddy",
			Data:      []byte("first output"),
		},
		{
			Operation: "second",
			Kind:      protocol.ProgressOutput,
			Source:    "caddy",
			Data:      []byte("second output"),
		},
		{
			Operation: "first",
			Kind:      protocol.ProgressComplete,
			Source:    "caddy",
			Text:      "First done",
		},
		{
			Operation: "second",
			Kind:      protocol.ProgressComplete,
			Source:    "caddy",
			Text:      "Second done",
		},
	}
	for _, event := range events {
		if err := service.publishProgress(1, event); err != nil {
			t.Fatal(err)
		}
	}

	if len(recorder.events) != len(events) {
		t.Fatalf(
			"received %d events, want %d: %#v",
			len(recorder.events),
			len(events),
			recorder.events,
		)
	}
	for index, event := range recorder.events {
		if event.Operation != events[index].Operation {
			t.Fatalf(
				"event %d operation = %q, want %q",
				index,
				event.Operation,
				events[index].Operation,
			)
		}
	}
}

func TestProgressFanoutDoesNotJoinOperationInProgress(t *testing.T) {
	t.Parallel()

	service := &Gateway{}
	first := &progressRecorder{}
	service.registerProgress(1, first)
	if err := service.publishProgress(1, protocol.AcquireProgress{
		Operation: "operation",
		Kind:      protocol.ProgressStep,
		Source:    "caddy",
		Text:      "Starting",
	}); err != nil {
		t.Fatal(err)
	}

	late := &progressRecorder{}
	service.registerProgress(1, late)
	if err := service.publishProgress(1, protocol.AcquireProgress{
		Operation: "operation",
		Kind:      protocol.ProgressOutput,
		Source:    "caddy",
		Data:      []byte("output"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishProgress(1, protocol.AcquireProgress{
		Operation: "operation",
		Kind:      protocol.ProgressComplete,
		Source:    "caddy",
		Text:      "Ready",
	}); err != nil {
		t.Fatal(err)
	}

	if len(first.events) != 3 {
		t.Fatalf("first acquisition events = %#v", first.events)
	}
	if len(late.events) != 0 {
		t.Fatalf("late acquisition events = %#v", late.events)
	}
}
