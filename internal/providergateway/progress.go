package providergateway

// Routes bounded provider startup events to the acquisitions waiting for a
// particular Caddy configuration revision.

import (
	"petris.dev/toby/internal/agent/progressio"
	"petris.dev/toby/internal/agent/protocol"
)

func startProgress(
	reporter ProgressReporter,
	source string,
	label string,
) *progressio.Operation {
	return progressio.Start(reporter, source, label)
}

type progressWaiter struct {
	revision   uint64
	reporter   ProgressReporter
	operations map[protocol.OperationID]bool
}

type revisionProgress struct {
	gateway  *Gateway
	revision uint64
}

var _ ProgressReporter = revisionProgress{}

func (r revisionProgress) Report(event protocol.AcquireProgress) error {
	return r.gateway.publishProgress(r.revision, event)
}

func (s *Gateway) registerProgress(
	revision uint64,
	reporter ProgressReporter,
) *progressWaiter {
	if reporter == nil {
		return nil
	}

	waiter := &progressWaiter{
		revision:   revision,
		reporter:   reporter,
		operations: make(map[protocol.OperationID]bool),
	}
	s.mu.Lock()
	s.progress = append(s.progress, waiter)
	s.mu.Unlock()

	return waiter
}

func (s *Gateway) unregisterProgress(waiter *progressWaiter) {
	if waiter == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for index, candidate := range s.progress {
		if candidate != waiter {
			continue
		}
		s.progress = append(
			s.progress[:index],
			s.progress[index+1:]...,
		)
		return
	}
}

func (s *Gateway) progressFor(revision uint64) ProgressReporter {
	return revisionProgress{gateway: s, revision: revision}
}

func (s *Gateway) publishProgress(
	revision uint64,
	event protocol.AcquireProgress,
) error {
	s.mu.Lock()
	waiters := make([]*progressWaiter, 0, len(s.progress))
	for _, waiter := range s.progress {
		if waiter.revision > revision {
			continue
		}

		running, known := waiter.operations[event.Operation]
		switch event.Kind {
		case protocol.ProgressStep:
			if known {
				continue
			}
			waiter.operations[event.Operation] = true
		case protocol.ProgressOutput:
			if !known || !running {
				continue
			}
		case protocol.ProgressComplete, protocol.ProgressFailure:
			if !known || !running {
				continue
			}
			waiter.operations[event.Operation] = false
		default:
			continue
		}
		waiters = append(waiters, waiter)
	}
	s.mu.Unlock()

	for _, waiter := range waiters {
		if err := waiter.reporter.Report(event); err != nil {
			s.logger.DebugError(
				"report models gateway progress",
				err,
				"revision", revision,
				"operation_id", event.Operation,
			)
			s.unregisterProgress(waiter)
		}
	}

	return nil
}
