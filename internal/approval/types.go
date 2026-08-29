package approval

// Approval record data, statuses, and the errors shared by the registry and
// its callers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"petris.dev/toby/internal/permission"
)

// Status is one approval record's lifecycle position.
type Status int

var _ fmt.Stringer = Status(0)

const (
	// StatusPending awaits the user's decision.
	StatusPending Status = iota
	// StatusExecuting holds an approved request whose execution is running.
	StatusExecuting
	// StatusCompleted holds an executed request and its saved response.
	StatusCompleted
	// StatusDenied is the final refused state.
	StatusDenied
)

// String returns the canonical textual representation.
func (s Status) String() string {
	switch s {
	case StatusExecuting:
		return "executing"
	case StatusCompleted:
		return "completed"
	case StatusDenied:
		return "denied"
	default:
		return "pending"
	}
}

// Record is one held host-action request awaiting the user's decision.
// Approving executes exactly the held request; the identifying fields never
// change after creation.
type Record struct {
	ID        string
	Action    string
	Name      string
	Message   string
	RequestID json.RawMessage
	Params    json.RawMessage
	Created   time.Time
}

// Request describes an action asking for authorization. Default is the caller's
// policy when nothing is configured for the action; RequestID and Params are the
// raw request fields held for deferred execution when the policy asks.
type Request struct {
	Action    string
	Name      string
	Message   string
	Default   permission.Rule
	RequestID json.RawMessage
	Params    json.RawMessage
}

// ErrDenied reports that policy or the user refused the action.
var ErrDenied = errors.New("approval denied")

// ErrNotFound reports an unknown or already-swept approval id.
var ErrNotFound = errors.New("approval not found")

// PendingError reports that an approval record must be decided before the
// action runs.
type PendingError struct {
	ID      string
	Name    string
	Message string
}

// Error returns the human-readable failure message.
func (e *PendingError) Error() string {
	return fmt.Sprintf("approval %s required: %s (%s)", e.ID, e.Name, e.Message)
}
