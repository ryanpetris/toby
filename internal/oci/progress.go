package oci

// Aggregates concurrent artifact reads into throttled absolute progress.

import (
	"fmt"
	"io"
	"sync"
	"time"
)

const progressReportInterval = 100 * time.Millisecond

type transferProgress struct {
	mu sync.Mutex

	phase     ProgressPhase
	reporter  ProgressReporter
	sizes     []int64
	completed []int64
	done      []bool
	last      time.Time
	err       error
}

func newTransferProgress(
	phase ProgressPhase,
	sizes []int64,
	reporter ProgressReporter,
) (*transferProgress, error) {
	copied := append([]int64(nil), sizes...)
	for _, size := range copied {
		if size < 0 {
			return nil, fmt.Errorf(
				"OCI transfer artifact size must not be negative",
			)
		}
	}

	progress := &transferProgress{
		phase:     phase,
		reporter:  reporter,
		sizes:     copied,
		completed: make([]int64, len(copied)),
		done:      make([]bool, len(copied)),
	}
	if err := progress.reportLocked(true); err != nil {
		return nil, err
	}

	return progress, nil
}

func reportProgress(
	reporter ProgressReporter,
	progress Progress,
) error {
	if reporter == nil {
		return nil
	}
	return reporter(progress)
}

func (p *transferProgress) add(index int, count int64) error {
	if count <= 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if index < 0 || index >= len(p.sizes) {
		return fmt.Errorf("OCI progress artifact index %d is invalid", index)
	}

	p.completed[index] = min(
		p.sizes[index],
		p.completed[index]+count,
	)
	return p.reportLocked(false)
}

func (p *transferProgress) complete(index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	if index < 0 || index >= len(p.sizes) {
		return fmt.Errorf("OCI progress artifact index %d is invalid", index)
	}

	p.completed[index] = p.sizes[index]
	p.done[index] = true
	return p.reportLocked(true)
}

func (p *transferProgress) finish() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}

	for index := range p.sizes {
		p.completed[index] = p.sizes[index]
		p.done[index] = true
	}
	return p.reportLocked(true)
}

func (p *transferProgress) reportLocked(force bool) error {
	if p.reporter == nil {
		return nil
	}

	now := time.Now()
	if !force &&
		!p.last.IsZero() &&
		now.Sub(p.last) < progressReportInterval {
		return nil
	}

	var snapshot Progress
	snapshot.Phase = p.phase
	snapshot.TotalItems = int64(len(p.sizes))
	for index, size := range p.sizes {
		snapshot.TotalBytes += size
		snapshot.CompletedBytes += p.completed[index]
		if p.done[index] {
			snapshot.CompletedItems++
		}
	}

	p.last = now
	if err := p.reporter(snapshot); err != nil {
		p.err = err
		return err
	}

	return nil
}

type progressReadCloser struct {
	io.ReadCloser

	progress *transferProgress
	index    int
}

var _ io.ReadCloser = (*progressReadCloser)(nil)

func (r *progressReadCloser) Read(data []byte) (int, error) {
	count, readErr := r.ReadCloser.Read(data)
	if count != 0 {
		if err := r.progress.add(r.index, int64(count)); err != nil {
			return count, err
		}
	}
	if readErr == io.EOF {
		if err := r.progress.complete(r.index); err != nil {
			return count, err
		}
	}

	return count, readErr
}
