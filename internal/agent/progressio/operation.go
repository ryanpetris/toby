// Package progressio reports agent-owned startup work through
// operation-scoped progress events.
package progressio

import (
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
)

// Reporter receives one acquisition progress event.
type Reporter interface {
	// Report records one acquisition progress event.
	Report(protocol.AcquireProgress) error
}

// Operation owns the identity and terminal state of one agent startup action.
type Operation struct {
	reporter Reporter
	id       protocol.OperationID
	source   string

	mu       sync.Mutex
	finished bool
}

// Start begins one operation and reports its safe user-facing label.
func Start(
	reporter Reporter,
	source string,
	label string,
) *Operation {
	operation := &Operation{
		reporter: reporter,
		id:       protocol.NewOperationID(),
		source:   source,
	}
	if reporter == nil {
		return operation
	}
	if err := reporter.Report(protocol.AcquireProgress{
		Operation: operation.id,
		Kind:      protocol.ProgressStep,
		Source:    source,
		Text:      label,
	}); err != nil {
		diagnostic.DiscardError(
			"progress reporting is optional",
			"report operation start",
			err,
			"operation_id", operation.id,
			"source", source,
		)
		operation.reporter = nil
	}

	return operation
}

// Complete reports successful completion once.
func (o *Operation) Complete(label string) {
	o.finish(protocol.ProgressComplete, label)
}

// Fail reports failed completion once.
func (o *Operation) Fail(label string) {
	o.finish(protocol.ProgressFailure, label)
}

func (o *Operation) finish(
	kind protocol.ProgressKind,
	label string,
) {
	if o == nil {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.finished = true
	if o.reporter == nil {
		return
	}

	err := o.reporter.Report(protocol.AcquireProgress{
		Operation: o.id,
		Kind:      kind,
		Source:    o.source,
		Text:      label,
	})
	if err != nil {
		diagnostic.DiscardError(
			"progress reporting is optional",
			"report operation completion",
			err,
			"operation_id", o.id,
			"source", o.source,
		)
		o.reporter = nil
	}

}
