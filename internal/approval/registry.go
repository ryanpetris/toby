package approval

// In-memory collection of one launch's approval records: creation with
// dedup, listing, deciding, one-shot grant redemption, saved responses, and
// blocking waits.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

const (
	approvalIDBytes = 6
	// maxLiveRecords bounds one launch's retained approvals fail-closed.
	maxLiveRecords = 64
)

type record struct {
	Record

	status   Status
	redeemed bool
	response []byte
	decided  chan struct{}
}

// Registry holds one launch's approval records. All methods are safe for
// concurrent use.
type Registry struct {
	mu      sync.Mutex
	records map[string]*record
}

// NewRegistry creates an empty approval registry.
func NewRegistry() *Registry {
	return &Registry{records: map[string]*record{}}
}

// Create records one held request awaiting a decision and returns it. An
// existing pending record holding the same action and parameter bytes is
// reused, so repeated identical requests share one approval id.
func (r *Registry) Create(req Request) (Record, error) {
	if r == nil {
		return Record{}, fmt.Errorf("approval registry is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, current := range r.records {
		if current.status == StatusPending &&
			current.Action == req.Action &&
			bytes.Equal(current.Params, req.Params) {
			return current.Record, nil
		}
	}
	if len(r.records) >= maxLiveRecords {
		return Record{}, fmt.Errorf(
			"launch already holds %d approvals",
			maxLiveRecords,
		)
	}

	id, err := r.newIDLocked()
	if err != nil {
		return Record{}, err
	}
	created := &record{
		Record: Record{
			ID:        id,
			Action:    req.Action,
			Name:      req.Name,
			Message:   req.Message,
			RequestID: append([]byte(nil), req.RequestID...),
			Params:    append([]byte(nil), req.Params...),
			Created:   time.Now(),
		},
		status:  StatusPending,
		decided: make(chan struct{}),
	}
	r.records[id] = created

	return created.Record, nil
}

// Pending returns the undecided records, oldest first.
func (r *Registry) Pending() []Record {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var pending []Record
	for _, current := range r.records {
		if current.status == StatusPending {
			pending = append(pending, current.Record)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Created.Equal(pending[j].Created) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].Created.Before(pending[j].Created)
	})

	return pending
}

// Approve moves a pending record into the executing state. changed is false
// when the record was already decided; the returned status reports its current
// state either way. Exactly one caller observes changed=true, so a decided
// record executes at most once.
func (r *Registry) Approve(id string) (Record, Status, bool, error) {
	return r.decide(id, StatusExecuting)
}

// Deny finally refuses a pending record and wakes its waiters. changed is
// false when the record was already decided.
func (r *Registry) Deny(id string) (Record, Status, bool, error) {
	return r.decide(id, StatusDenied)
}

func (r *Registry) decide(id string, next Status) (Record, Status, bool, error) {
	if r == nil {
		return Record{}, StatusPending, false, fmt.Errorf("approval registry is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.records[id]
	if !ok {
		return Record{}, StatusPending, false, ErrNotFound
	}
	if current.status != StatusPending {
		return current.Record, current.status, false, nil
	}

	current.status = next
	if next == StatusDenied {
		close(current.decided)
	}

	return current.Record, current.status, true, nil
}

// RedeemGrant authorizes exactly one execution of an approved record. It fails
// closed unless the record is executing its approval for the same action and
// has not redeemed it yet.
func (r *Registry) RedeemGrant(id, action string) error {
	if r == nil {
		return fmt.Errorf("approval registry is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.records[id]
	if !ok {
		return ErrNotFound
	}
	if current.status != StatusExecuting ||
		current.redeemed ||
		current.Action != action {
		return ErrDenied
	}

	current.redeemed = true
	return nil
}

// SaveResponse completes an executing record with the response of its held
// request and wakes its waiters. The response is retained for later waits.
func (r *Registry) SaveResponse(id string, response []byte) error {
	if r == nil {
		return fmt.Errorf("approval registry is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.records[id]
	if !ok {
		return ErrNotFound
	}
	if current.status != StatusExecuting {
		return fmt.Errorf(
			"approval %s is %s, want executing",
			id,
			current.status,
		)
	}

	current.status = StatusCompleted
	current.response = append([]byte(nil), response...)
	close(current.decided)

	return nil
}

// Wait blocks until the record is completed or denied, or until timeout
// elapses; a timeout reports StatusPending so the caller can wait again. A
// completed record returns its saved response on every wait.
func (r *Registry) Wait(
	ctx context.Context,
	id string,
	timeout time.Duration,
) (Record, Status, []byte, error) {
	if r == nil {
		return Record{}, StatusPending, nil, fmt.Errorf("approval registry is nil")
	}

	r.mu.Lock()
	current, ok := r.records[id]
	r.mu.Unlock()
	if !ok {
		return Record{}, StatusPending, nil, ErrNotFound
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-current.decided:
	case <-timer.C:
		return current.Record, StatusPending, nil, nil
	case <-ctx.Done():
		return current.Record, StatusPending, nil, ctx.Err()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if current.status == StatusCompleted {
		return current.Record,
			StatusCompleted,
			append([]byte(nil), current.response...),
			nil
	}
	return current.Record, current.status, nil, nil
}

// DenyAll refuses every undecided record and wakes all waiters. The launch
// teardown calls it after in-flight executions have been joined.
func (r *Registry) DenyAll() {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, current := range r.records {
		if current.status == StatusPending || current.status == StatusExecuting {
			current.status = StatusDenied
			close(current.decided)
		}
	}
}

func (r *Registry) newIDLocked() (string, error) {
	for {
		raw := make([]byte, approvalIDBytes)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate approval id: %w", err)
		}
		id := hex.EncodeToString(raw)
		if _, exists := r.records[id]; !exists {
			return id, nil
		}
	}
}
