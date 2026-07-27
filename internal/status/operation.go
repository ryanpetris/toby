package status

// Operation-scoped output and completion handles for startup work.

import (
	"io"
	"sync"
)

// Operation owns the presentation identity and output writers for one startup
// action.
type Operation struct {
	service *Service
	id      OperationID

	mu       sync.Mutex
	finished bool
}

// Writer returns an output sink permanently associated with this operation.
func (o *Operation) Writer(destination io.Writer) io.Writer {
	if o == nil || o.service == nil {
		if destination == nil {
			return io.Discard
		}
		return destination
	}

	return o.service.operationWriter(o.id, destination)
}

// SetLabel replaces the running operation's safe presentation label.
func (o *Operation) SetLabel(label string) {
	if o == nil || o.service == nil {
		return
	}

	o.mu.Lock()
	finished := o.finished
	o.mu.Unlock()
	if finished {
		return
	}

	o.service.setOperationLabel(o.id, label)
}

// SetProgress replaces the operation's absolute progress snapshot.
func (o *Operation) SetProgress(progress Progress) {
	if o == nil || o.service == nil {
		return
	}

	o.mu.Lock()
	finished := o.finished
	o.mu.Unlock()
	if finished {
		return
	}

	o.service.setOperationProgress(o.id, progress)
}

// StartChild begins a child action under this operation. The child inherits the
// parent's presentation scope and temporarily replaces the parent in the
// interactive display while retaining its own output transcript.
func (o *Operation) StartChild(label string) *Operation {
	if o == nil || o.service == nil {
		return nil
	}

	o.mu.Lock()
	finished := o.finished
	o.mu.Unlock()
	if finished {
		return nil
	}

	return o.service.startChildOperation(o.id, label)
}

// Finish completes the operation, marking it failed when err is non-nil.
func (o *Operation) Finish(err error) {
	o.finish(err != nil, "")
}

// Complete completes the operation and optionally supplies a safe terminal
// label for append-only presentation modes.
func (o *Operation) Complete(label string) {
	o.finish(false, label)
}

func (o *Operation) finish(failed bool, terminalLabel string) {
	if o == nil || o.service == nil {
		return
	}

	o.mu.Lock()
	if o.finished {
		o.mu.Unlock()
		return
	}
	o.finished = true
	o.mu.Unlock()

	o.service.closeOperationWriters(o.id)
	o.service.finishOperation(o.id, failed, terminalLabel)
}

// Close completes an unfinished operation.
func (o *Operation) Close() error {
	o.Finish(nil)
	return nil
}

var _ io.Closer = (*Operation)(nil)
