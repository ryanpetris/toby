//go:build linux

package bwrap

// Buffers output until payload provenance is established, then either commits
// it verbatim or discards one replay-safe pre-payload attempt.

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

type payloadOutputGate struct {
	mu sync.Mutex

	output      io.Writer
	buffer      []byte
	attached    bool
	committed   bool
	passthrough bool
	replaySafe  bool
	err         error
	failures    chan error
}

var _ io.Writer = (*payloadOutputGate)(nil)

func newPayloadOutputGate() *payloadOutputGate {
	return &payloadOutputGate{
		replaySafe: true,
		failures:   make(chan error, 1),
	}
}

func (g *payloadOutputGate) Write(data []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.err != nil {
		return 0, g.err
	}
	if g.passthrough {
		return g.writeLocked(data)
	}
	if len(g.buffer)+len(data) <= maxPrePayloadOutput {
		g.buffer = append(g.buffer, data...)
		return len(data), nil
	}

	g.replaySafe = false
	if !g.attached {
		err := fmt.Errorf(
			"payload output exceeded %d bytes before its sink was attached",
			maxPrePayloadOutput,
		)
		g.failLocked(err)
		return 0, err
	}
	if err := g.flushLocked(); err != nil {
		return 0, err
	}
	g.passthrough = true

	return g.writeLocked(data)
}

func (g *payloadOutputGate) attach(output io.Writer) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.attached {
		return fmt.Errorf("payload output sink is already attached")
	}
	if output == nil {
		output = io.Discard
	}
	g.output = output
	g.attached = true
	if !g.committed || g.passthrough {
		return g.err
	}

	if err := g.flushLocked(); err != nil {
		return err
	}
	g.passthrough = true

	return nil
}

func (g *payloadOutputGate) commit() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.committed = true
	g.replaySafe = false
	if g.err != nil || g.passthrough || !g.attached {
		return g.err
	}

	if err := g.flushLocked(); err != nil {
		return err
	}
	g.passthrough = true

	return nil
}

func (g *payloadOutputGate) rejectReplay(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.replaySafe = false
	g.failLocked(err)
}

func (g *payloadOutputGate) replayAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.replaySafe && g.err == nil
}

func (g *payloadOutputGate) finish(flush bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if flush && !g.passthrough {
		if !g.attached && len(g.buffer) > 0 {
			g.failLocked(fmt.Errorf(
				"payload output sink was not attached",
			))
		} else if g.attached {
			if flushErr := g.flushLocked(); flushErr != nil &&
				g.err == nil {
				g.failLocked(flushErr)
			}
		}
	}
	g.buffer = nil
	g.replaySafe = false

	return g.err
}

func (g *payloadOutputGate) flushLocked() error {
	if len(g.buffer) == 0 {
		return g.err
	}

	data := g.buffer
	g.buffer = nil
	_, err := g.writeLocked(data)
	return err
}

func (g *payloadOutputGate) writeLocked(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, g.err
	}
	if g.err != nil {
		return 0, g.err
	}

	count, err := g.output.Write(data)
	if err == nil && count != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		g.failLocked(err)
	}

	return count, err
}

func (g *payloadOutputGate) failLocked(err error) {
	if err == nil {
		return
	}
	g.err = errors.Join(g.err, err)
	select {
	case g.failures <- err:
	default:
	}
}
