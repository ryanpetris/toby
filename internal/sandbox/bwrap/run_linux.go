//go:build linux

package bwrap

// Coordinates the sequential Bubblewrap processes in one run while retaining
// its unique root overlay and exact host capabilities.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/diagnostic"
)

// ProcessExecutor is the run-scoped command execution dependency. Executor is
// the production implementation; the interface keeps run assembly testable
// without spawning Bubblewrap.
type ProcessExecutor interface {
	// Execute runs one prepared Bubblewrap invocation.
	Execute(context.Context, *Invocation, ProcessIO) (int, error)
}

var _ ProcessExecutor = (*Executor)(nil)

// Run owns one run-local overlay and descriptor set. commandMu serializes only
// lifecycle/application commands belonging to this run, while mu protects
// short-lived state and source access needed concurrently by host capabilities.
// Neither mutex locks a private home, application identity, or another run.
type Run struct {
	commandMu   sync.Mutex
	mu          sync.RWMutex
	plan        Plan
	sources     *retainedSources
	directories *RunDirectories
	executor    ProcessExecutor
	logger      *diagnostic.Logger
	invoked     bool
	closed      bool
}

var _ io.Closer = (*Run)(nil)

// NewRun validates and retains one complete run. Ownership of directories
// transfers only on success. Caller-owned source descriptors remain open and
// higher-level OCI/storage leases must outlive the returned Run.
func NewRun(
	plan Plan,
	sources Sources,
	directories *RunDirectories,
	executor ProcessExecutor,
	logger *diagnostic.Logger,
) (*Run, error) {
	if directories == nil {
		return nil, fmt.Errorf("bubblewrap run directories are required")
	}
	if executor == nil {
		return nil, fmt.Errorf("bubblewrap process executor is required")
	}

	canonical := plan.Canonical()
	if err := canonical.Validate(); err != nil {
		return nil, err
	}
	if directories.ID() != canonical.RunID {
		return nil, fmt.Errorf(
			"run directory ID %q does not match plan ID %q",
			directories.ID(),
			canonical.RunID,
		)
	}
	if got := directories.Overlay(); got != canonical.Overlay {
		return nil, fmt.Errorf(
			"run directory overlay %#v does not match plan overlay %#v",
			got,
			canonical.Overlay,
		)
	}
	if err := validateRunDirectorySources(
		directories,
		sources,
		logger,
	); err != nil {
		return nil, err
	}

	retained, err := retainPlanSources(canonical, sources)
	if err != nil {
		return nil, fmt.Errorf("retain Bubblewrap run sources: %w", err)
	}

	return &Run{
		plan:        canonical,
		sources:     retained,
		directories: directories,
		executor:    executor,
		logger:      logger,
	}, nil
}

// Plan returns a detached copy of the run's fixed plan with its initial
// command.
func (r *Run) Plan() Plan {
	if r == nil {
		return Plan{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return Plan{}
	}

	return r.plan.Clone()
}

// Execute launches one command against the run's shared overlay. Calls on the
// same Run are sequential; independent Runs remain fully concurrent.
func (r *Run) Execute(
	ctx context.Context,
	command Command,
	streams ProcessIO,
) (code int, returnErr error) {
	if r == nil {
		return 1, fmt.Errorf("bubblewrap run is nil")
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	r.mu.RLock()
	if r.closed || r.sources == nil || r.executor == nil {
		r.mu.RUnlock()
		return 1, fmt.Errorf("bubblewrap run is closed")
	}

	plan := r.plan.Clone()
	plan.Command = command
	sources, err := r.sources.current()
	if err != nil {
		r.mu.RUnlock()
		return 1, fmt.Errorf("access Bubblewrap run sources: %w", err)
	}
	invocation, err := Render(plan, sources)
	executor := r.executor
	r.mu.RUnlock()
	if err != nil {
		return 1, err
	}
	defer func() {
		r.logger.DebugError(
			"close Bubblewrap invocation",
			invocation.Close(),
		)
	}()
	invocation.allowOverlayReuseRetry = r.invoked

	code, executeErr := executor.Execute(ctx, invocation, streams)
	// Even a failed Bubblewrap setup can have mounted this overlay before
	// teardown, so every attempted process makes later reuse retry-eligible.
	r.invoked = true

	return code, executeErr
}

// Close releases retained sources and removes the exact transient root
// overlay through bounded cleanup batches.
func (r *Run) Close() error {
	if r == nil {
		return nil
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	r.logger.DebugError(
		"close Bubblewrap run sources",
		r.sources.Close(),
	)
	r.logger.DebugError(
		"remove Bubblewrap run directories",
		r.directories.Close(),
	)

	r.closed = true
	r.sources = nil
	r.directories = nil
	r.executor = nil

	return nil
}

func validateRunDirectorySources(
	directories *RunDirectories,
	sources Sources,
	logger *diagnostic.Logger,
) error {
	upper, err := directories.UpperFile()
	if err != nil {
		return fmt.Errorf("open retained run upper: %w", err)
	}
	defer func() {
		logger.DebugError(
			"close retained run upper descriptor",
			upper.Close(),
		)
	}()
	work, err := directories.WorkFile()
	if err != nil {
		return fmt.Errorf("open retained run work: %w", err)
	}
	defer func() {
		logger.DebugError(
			"close retained run work descriptor",
			work.Close(),
		)
	}()

	upperInfo, err := upper.Stat()
	if err != nil {
		return fmt.Errorf("inspect retained run upper: %w", err)
	}
	sourceUpperInfo, err := descriptorInfo(
		"overlay upper",
		sources.OverlayUpper,
	)
	if err != nil {
		return err
	}
	if !os.SameFile(upperInfo, sourceUpperInfo) {
		return fmt.Errorf(
			"overlay upper source does not match retained run directory",
		)
	}

	workInfo, err := work.Stat()
	if err != nil {
		return fmt.Errorf("inspect retained run work: %w", err)
	}
	sourceWorkInfo, err := descriptorInfo("overlay work", sources.OverlayWork)
	if err != nil {
		return err
	}
	if !os.SameFile(workInfo, sourceWorkInfo) {
		return fmt.Errorf(
			"overlay work source does not match retained run directory",
		)
	}

	return nil
}
