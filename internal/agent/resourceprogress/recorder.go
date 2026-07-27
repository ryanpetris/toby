// Package resourceprogress records agent resource progress in persistent logs.
package resourceprogress

// Persists agent-generation progress directly to one bounded JSONL resource
// log without retaining an in-memory transcript.

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/diagnostic"
)

const defaultMaximumBytes = 64 << 20

type record struct {
	GenerationID protocol.OperationID      `json:"generation_id"`
	Sequence     uint64                    `json:"sequence"`
	Kind         string                    `json:"kind"`
	Progress     *protocol.AcquireProgress `json:"progress,omitempty"`
}

// Recorder writes one startup generation and its terminal state.
type Recorder struct {
	file       *os.File
	generation protocol.OperationID
	maxBytes   int64
	logger     *diagnostic.Logger

	mu        sync.Mutex
	size      int64
	sequence  uint64
	truncated bool
	finished  bool
}

// New creates one generation log for a single agent resource.
func New(
	logs *resourcelog.Service,
	logger *diagnostic.Logger,
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
) *Recorder {
	generation := protocol.NewOperationID()
	recorder := &Recorder{
		generation: generation,
		maxBytes:   defaultMaximumBytes,
		logger:     logger,
	}
	if logs == nil {
		return recorder
	}

	file, err := logs.Create(kind, resourceID, generation)
	if err != nil {
		logger.DebugError(
			"create resource progress log",
			err,
			"resource_kind",
			kind,
			"resource_id",
			resourceID,
			"generation_id",
			generation,
		)
		return recorder
	}
	recorder.file = file

	return recorder
}

// Report appends one exact progress event.
func (r *Recorder) Report(event protocol.AcquireProgress) error {
	if r == nil {
		return fmt.Errorf("resource progress recorder is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return fmt.Errorf("resource progress generation is finished")
	}

	return r.appendLocked(record{
		Kind:     "progress",
		Progress: &event,
	}, true)
}

// Finish appends and syncs one terminal generation record, then closes the
// log. Repeated calls return the first completion state without rewriting it.
func (r *Recorder) Finish(operationErr error) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return nil
	}
	r.finished = true
	if r.file == nil {
		r.mu.Unlock()
		return nil
	}
	kind := "complete"
	if operationErr != nil {
		kind = "failed"
	}
	appendErr := r.appendLocked(record{Kind: kind}, false)
	syncErr := r.file.Sync()
	file := r.file
	r.file = nil
	r.mu.Unlock()

	r.logger.DebugError("write terminal resource progress", appendErr)
	r.logger.DebugError("sync resource progress log", syncErr)
	r.logger.DebugError("close resource progress log", file.Close())
	return nil
}

func (r *Recorder) appendLocked(
	item record,
	bounded bool,
) error {
	if bounded && r.truncated {
		return nil
	}

	r.sequence++
	item.GenerationID = r.generation
	item.Sequence = r.sequence
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if bounded && r.size+int64(len(encoded)) > r.maxBytes {
		r.sequence--
		r.truncated = true
		return r.appendLocked(record{Kind: "truncated"}, false)
	}
	if r.file == nil {
		return nil
	}

	written, err := r.file.WriteAt(encoded, r.size)
	if err != nil {
		r.logger.DebugError("write resource progress log", err)
		r.logger.DebugError(
			"close failed resource progress log",
			r.file.Close(),
		)
		r.file = nil
		return nil
	}
	if written != len(encoded) {
		r.logger.Debug(
			"short resource progress log write",
			"written_bytes",
			written,
			"expected_bytes",
			len(encoded),
		)
		r.logger.DebugError(
			"close failed resource progress log",
			r.file.Close(),
		)
		r.file = nil
		return nil
	}
	r.size += int64(written)

	return nil
}
