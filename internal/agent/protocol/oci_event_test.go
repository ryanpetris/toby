package protocol

// Verifies transport-independent OCI event shape validation.

import "testing"

func TestOCIEventValidate(t *testing.T) {
	progress := OCIProgressState{
		Phase:      OCIProgressDownloading,
		TotalBytes: 1,
	}
	valid := []OCIEvent{
		{
			Kind:        OCIEventAccepted,
			OperationID: "operation",
		},
		{
			Kind:        OCIEventSnapshot,
			OperationID: "operation",
			Sequence:    1,
			Progress:    &progress,
		},
		{
			Kind:        OCIEventOutput,
			OperationID: "operation",
			Sequence:    2,
			Source:      OCISourceRegistry,
			Stream:      OutputStderr,
			Data:        []byte("output"),
		},
		{
			Kind:        OCIEventComplete,
			OperationID: "operation",
			Sequence:    3,
		},
		{
			Kind:        OCIEventFailed,
			OperationID: "operation",
			Sequence:    3,
			Message:     "failed",
		},
	}
	for _, event := range valid {
		if err := event.Validate(); err != nil {
			t.Fatalf("validate %q event: %v", event.Kind, err)
		}
	}

	invalid := []OCIEvent{
		{Kind: OCIEventAccepted},
		{
			Kind:        OCIEventSnapshot,
			OperationID: "operation",
			Sequence:    1,
		},
		{
			Kind:        OCIEventOutput,
			OperationID: "operation",
			Sequence:    1,
			Source:      OCISourceRegistry,
			Stream:      OutputStderr,
		},
		{
			Kind:        OCIEventFailed,
			OperationID: "operation",
			Sequence:    1,
		},
	}
	for _, event := range invalid {
		if err := event.Validate(); err == nil {
			t.Fatalf("invalid %q event succeeded", event.Kind)
		}
	}
}
