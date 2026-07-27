package prepareclient

// Tests full-reference OCI progress presentation.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/status"
)

func TestFollowStreamCreatesStatusOnlyForWorkOrFailure(t *testing.T) {
	const operationID = protocol.OperationID("operation")

	for _, test := range []struct {
		name       string
		events     []protocol.OCIEvent
		receiveErr error
		wantStarts int
		wantOutput string
		wantError  string
	}{
		{
			name: "cached",
			events: []protocol.OCIEvent{
				{
					Kind:        protocol.OCIEventAccepted,
					OperationID: operationID,
				},
				{
					Kind:        protocol.OCIEventComplete,
					OperationID: operationID,
					Sequence:    1,
					Cached:      true,
				},
			},
		},
		{
			name: "download progress",
			events: []protocol.OCIEvent{
				{
					Kind:        protocol.OCIEventAccepted,
					OperationID: operationID,
				},
				{
					Kind:        protocol.OCIEventProgress,
					OperationID: operationID,
					Sequence:    1,
					Progress: &protocol.OCIProgressState{
						Phase: protocol.OCIProgressDownloading,
					},
				},
				{
					Kind:        protocol.OCIEventComplete,
					OperationID: operationID,
					Sequence:    2,
				},
			},
			wantStarts: 1,
		},
		{
			name: "attached snapshot",
			events: []protocol.OCIEvent{
				{
					Kind:        protocol.OCIEventAccepted,
					OperationID: operationID,
				},
				{
					Kind:        protocol.OCIEventSnapshot,
					OperationID: operationID,
					Sequence:    7,
					Progress: &protocol.OCIProgressState{
						Phase: protocol.OCIProgressExtracting,
					},
				},
				{
					Kind:        protocol.OCIEventComplete,
					OperationID: operationID,
					Sequence:    8,
				},
			},
			wantStarts: 1,
		},
		{
			name: "output before progress",
			events: []protocol.OCIEvent{
				{
					Kind:        protocol.OCIEventAccepted,
					OperationID: operationID,
				},
				{
					Kind:        protocol.OCIEventOutput,
					OperationID: operationID,
					Sequence:    1,
					Stream:      protocol.OutputStdout,
					Data:        []byte("pull output"),
				},
				{
					Kind:        protocol.OCIEventComplete,
					OperationID: operationID,
					Sequence:    2,
				},
			},
			wantStarts: 1,
			wantOutput: "pull output",
		},
		{
			name: "agent failure",
			events: []protocol.OCIEvent{
				{
					Kind:        protocol.OCIEventAccepted,
					OperationID: operationID,
				},
				{
					Kind:        protocol.OCIEventFailed,
					OperationID: operationID,
					Sequence:    1,
					Message:     "pull failed",
				},
			},
			wantStarts: 1,
			wantError:  "pull failed",
		},
		{
			name:       "stream failure",
			receiveErr: errors.New("stream failed"),
			wantStarts: 1,
			wantError:  "stream failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &recordedEventStream{
				events:     test.events,
				receiveErr: test.receiveErr,
			}
			var output bytes.Buffer
			starts := 0
			err := followStream(
				"example.test/image:latest",
				stream,
				Presentation{
					Start: func() *status.Operation {
						starts++
						return &status.Operation{}
					},
					Stdout: &output,
				},
			)

			if starts != test.wantStarts {
				t.Errorf(
					"status starts = %d, want %d",
					starts,
					test.wantStarts,
				)
			}
			if output.String() != test.wantOutput {
				t.Errorf(
					"output = %q, want %q",
					output.String(),
					test.wantOutput,
				)
			}
			if !stream.closed {
				t.Error("event stream was not closed")
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil ||
				!strings.Contains(err.Error(), test.wantError) {
				t.Fatalf(
					"error = %v, want containing %q",
					err,
					test.wantError,
				)
			}
		})
	}
}

func TestApplyProgressRetainsFullReference(t *testing.T) {
	const reference = "registry.example/very/long/image:latest"
	operation := &recordedOperation{}

	applyProgress(
		operation,
		reference,
		protocol.OCIProgressState{
			Phase:          protocol.OCIProgressDownloading,
			CompletedBytes: 3,
			TotalBytes:     7,
		},
	)

	if operation.label != "Pulling OCI image "+reference {
		t.Fatalf("label = %q", operation.label)
	}
	if operation.progress.OCIAction != "Pulling" ||
		operation.progress.OCIReference != reference {
		t.Fatalf("progress = %#v", operation.progress)
	}
}

type recordedEventStream struct {
	events     []protocol.OCIEvent
	receiveErr error
	index      int
	closed     bool
}

func (s *recordedEventStream) Recv() (protocol.OCIEvent, error) {
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.receiveErr != nil {
		err := s.receiveErr
		s.receiveErr = nil
		return protocol.OCIEvent{}, err
	}

	return protocol.OCIEvent{}, io.EOF
}

func (s *recordedEventStream) Close() error {
	s.closed = true
	return nil
}

type recordedOperation struct {
	label    string
	progress status.Progress
}

func (o *recordedOperation) SetLabel(label string) {
	o.label = label
}

func (o *recordedOperation) SetProgress(progress status.Progress) {
	o.progress = progress
}
