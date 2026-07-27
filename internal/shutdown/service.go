package shutdown

// Coordinates operating-system and agent-initiated shutdown without
// interrupting an active startup operation.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"

	"go.uber.org/fx"
)

// Service owns the process signal subscription and command lifetime.
type Service struct {
	logger *diagnostic.Logger

	context context.Context
	cancel  context.CancelCauseFunc
	signals chan os.Signal
	done    chan struct{}

	mu                sync.Mutex
	startup           bool
	pending           syscall.Signal
	foreground        func(syscall.Signal) error
	deadline          time.Time
	forceCancel       *time.Timer
	stopped           bool
	notificationsOnce sync.Once
}

// NewService constructs the process-wide shutdown service.
func NewService(
	lifecycle fx.Lifecycle,
	diagnostics *diagnostic.Service,
) *Service {
	ctx, cancel := context.WithCancelCause(context.Background())
	service := &Service{
		logger:  diagnostics.Logger("shutdown"),
		context: ctx,
		cancel:  cancel,
		signals: make(chan os.Signal, 2),
		done:    make(chan struct{}),
	}

	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			signal.Notify(
				service.signals,
				syscall.SIGINT,
				syscall.SIGTERM,
			)
			go service.listen()
			return nil
		},
		OnStop: func(context.Context) error {
			service.stop()
			return nil
		},
	})

	return service
}

// Context is canceled when the current command should begin teardown.
func (s *Service) Context() context.Context {
	if s == nil {
		return context.Background()
	}
	return s.context
}

// BeginStartup defers signal cancellation until the current startup operation
// reaches a checkpoint.
func (s *Service) BeginStartup() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.startup {
		return fmt.Errorf("startup shutdown deferral is already active")
	}
	if cause := context.Cause(s.context); cause != nil {
		return cause
	}
	s.startup = true
	return nil
}

// Checkpoint stops startup before another operation begins when shutdown was
// requested during the operation that just completed.
func (s *Service) Checkpoint() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	pending := s.pending
	if pending != 0 {
		s.pending = 0
		s.cancel(signalExit(pending))
	}
	cause := context.Cause(s.context)
	s.mu.Unlock()

	return cause
}

// EndStartup leaves deferred startup mode. A pending signal becomes ordinary
// command cancellation unless foreground ownership has already begun.
func (s *Service) EndStartup() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	s.startup = false
	pending := s.pending
	if pending != 0 && s.foreground == nil {
		s.pending = 0
		s.cancel(signalExit(pending))
	}
	cause := context.Cause(s.context)
	s.mu.Unlock()

	return cause
}

// RegisterForeground transfers SIGINT and SIGTERM delivery to one live
// foreground sandbox. The returned function restores ordinary command
// ownership.
func (s *Service) RegisterForeground(
	handler func(syscall.Signal) error,
) func() {
	if s == nil || handler == nil {
		return func() {}
	}

	s.mu.Lock()
	s.startup = false
	s.foreground = handler
	pending := s.pending
	s.pending = 0
	if pending != 0 {
		s.forwardLocked(pending)
	}
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.foreground = nil
			s.mu.Unlock()
		})
	}
}

// RequestAgentStop interrupts a foreground sandbox and begins client teardown
// using the grace period advertised by the agent.
func (s *Service) RequestAgentStop(grace time.Duration) {
	if s == nil {
		return
	}
	if grace <= 0 {
		grace = ClientShutdownGrace
	}

	s.handle(syscall.SIGINT, grace)
}

// CleanupContext returns one context bounded by the controlling shutdown
// deadline. An agent-provided deadline takes precedence over the local
// default.
func (s *Service) CleanupContext() (
	context.Context,
	context.CancelFunc,
) {
	if s == nil {
		return context.WithTimeout(
			context.Background(),
			ClientShutdownGrace,
		)
	}

	s.mu.Lock()
	deadline := s.deadline
	s.mu.Unlock()
	if deadline.IsZero() {
		deadline = time.Now().Add(ClientShutdownGrace)
	}

	return context.WithDeadline(context.Background(), deadline)
}

func (s *Service) listen() {
	defer close(s.done)
	for current := range s.signals {
		typed, ok := current.(syscall.Signal)
		if !ok {
			continue
		}
		s.handle(typed, ClientShutdownGrace)
	}
}

func (s *Service) handle(
	current syscall.Signal,
	grace time.Duration,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped || context.Cause(s.context) != nil {
		return
	}
	requestedDeadline := time.Now().Add(grace)
	if s.deadline.IsZero() || requestedDeadline.Before(s.deadline) {
		s.deadline = requestedDeadline
	}
	if s.foreground != nil {
		s.forwardLocked(current)
		return
	}
	if s.startup {
		if s.pending == 0 {
			s.pending = current
		}
		return
	}

	s.cancel(signalExit(current))
}

func (s *Service) forwardLocked(current syscall.Signal) {
	if err := s.foreground(current); err != nil {
		s.logger.DebugError(
			"forward shutdown signal to foreground sandbox",
			err,
			"signal",
			current.String(),
		)
	}

	cancelAt := s.deadline.Add(-ClientSandboxReserve)
	delay := time.Until(cancelAt)
	if delay <= 0 {
		s.cancel(signalExit(current))
		return
	}
	if s.forceCancel != nil {
		s.forceCancel.Stop()
	}
	s.forceCancel = time.AfterFunc(delay, func() {
		s.cancel(signalExit(current))
	})
}

func (s *Service) stop() {
	if s == nil {
		return
	}

	s.notificationsOnce.Do(func() {
		signal.Stop(s.signals)
		close(s.signals)
	})

	s.mu.Lock()
	s.stopped = true
	if s.forceCancel != nil {
		s.forceCancel.Stop()
	}
	s.cancel(context.Canceled)
	s.mu.Unlock()

	<-s.done
}

func signalExit(current syscall.Signal) error {
	code := 1
	if current > 0 {
		code = 128 + int(current)
	}
	return errors.Join(
		exitcode.Code(code),
		context.Canceled,
	)
}
