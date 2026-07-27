package protocol

// Defines transport-independent OCI preparation events.

import "fmt"

// OCIEventKind identifies one event in a preparation operation.
type OCIEventKind string

const (
	// OCIEventAccepted acknowledges an OCI preparation request.
	OCIEventAccepted OCIEventKind = "accepted"
	// OCIEventSnapshot carries the latest known operation state.
	OCIEventSnapshot OCIEventKind = "snapshot"
	// OCIEventProgress carries byte and item progress.
	OCIEventProgress OCIEventKind = "progress"
	// OCIEventOutput carries subprocess output.
	OCIEventOutput OCIEventKind = "output"
	// OCIEventComplete reports successful preparation.
	OCIEventComplete OCIEventKind = "complete"
	// OCIEventFailed reports failed preparation.
	OCIEventFailed OCIEventKind = "failed"
)

// OCIEvent is one ordered, transport-independent preparation event.
type OCIEvent struct {
	Kind        OCIEventKind
	OperationID OperationID
	Sequence    uint64
	Progress    *OCIProgressState
	Source      OCISource
	Stream      OutputStream
	Data        []byte
	Cached      bool
	Message     string
}

// Validate checks the event shape before it crosses the agent boundary.
func (e OCIEvent) Validate() error {
	if err := ValidateOperationID(e.OperationID); err != nil {
		return fmt.Errorf("OCI event operation ID: %w", err)
	}

	switch e.Kind {
	case OCIEventAccepted:
		return nil
	case OCIEventSnapshot, OCIEventProgress:
		if err := validateOCIEventSequence(e.Sequence); err != nil {
			return err
		}
		if e.Progress == nil {
			return fmt.Errorf("OCI progress event is empty")
		}

		return e.Progress.validate()
	case OCIEventOutput:
		if err := validateOCIEventSequence(e.Sequence); err != nil {
			return err
		}
		if err := e.Source.validate(); err != nil {
			return err
		}
		if err := e.Stream.validate(); err != nil {
			return err
		}
		if len(e.Data) == 0 {
			return fmt.Errorf("OCI output event is empty")
		}
		if len(e.Data) > MaxProgressOutputBytes {
			return fmt.Errorf(
				"OCI output event exceeds %d bytes",
				MaxProgressOutputBytes,
			)
		}

		return nil
	case OCIEventComplete:
		return validateOCIEventSequence(e.Sequence)
	case OCIEventFailed:
		if err := validateOCIEventSequence(e.Sequence); err != nil {
			return err
		}
		if e.Message == "" {
			return fmt.Errorf("OCI failure message is empty")
		}
		if len(e.Message) > maxErrorMessageBytes {
			return fmt.Errorf(
				"OCI failure message exceeds %d bytes",
				maxErrorMessageBytes,
			)
		}

		return nil
	default:
		return fmt.Errorf("unknown OCI event kind %q", e.Kind)
	}
}

func validateOCIEventSequence(sequence uint64) error {
	if sequence == 0 {
		return fmt.Errorf("OCI event sequence must be positive")
	}

	return nil
}
