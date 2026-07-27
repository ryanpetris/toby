package shutdown

// Tests deferred startup cancellation, foreground signal ownership, and
// agent-provided cleanup deadlines.

import (
	"context"
	"syscall"
	"testing"
	"time"

	"petris.dev/toby/internal/diagnostic/exitcode"
)

func newTestService() *Service {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Service{
		context: ctx,
		cancel:  cancel,
	}
}

func TestStartupSignalWaitsForCheckpoint(t *testing.T) {
	service := newTestService()
	if err := service.BeginStartup(); err != nil {
		t.Fatal(err)
	}

	service.handle(syscall.SIGTERM, time.Second)
	select {
	case <-service.Context().Done():
		t.Fatal("startup signal canceled the active operation")
	default:
	}

	err := service.Checkpoint()
	if got := exitcode.FromError(err); got != 143 {
		t.Fatalf("checkpoint exit code = %d, want 143", got)
	}
	select {
	case <-service.Context().Done():
	default:
		t.Fatal("checkpoint did not cancel startup")
	}
}

func TestForegroundSignalUsesHandlerBeforeCancellation(t *testing.T) {
	service := newTestService()
	received := make(chan syscall.Signal, 1)
	unregister := service.RegisterForeground(
		func(current syscall.Signal) error {
			received <- current
			return nil
		},
	)
	defer unregister()

	grace := ClientSandboxReserve + 50*time.Millisecond
	service.handle(syscall.SIGINT, grace)
	select {
	case current := <-received:
		if current != syscall.SIGINT {
			t.Fatalf("foreground signal = %s, want interrupt", current)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground did not receive signal")
	}
	select {
	case <-service.Context().Done():
		t.Fatal("foreground context canceled before its sandbox reserve")
	default:
	}
	select {
	case <-service.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("foreground context did not reach its bounded deadline")
	}
}

func TestPendingStartupSignalTransfersToForeground(t *testing.T) {
	service := newTestService()
	if err := service.BeginStartup(); err != nil {
		t.Fatal(err)
	}
	service.handle(
		syscall.SIGTERM,
		ClientSandboxReserve+time.Second,
	)

	received := make(chan syscall.Signal, 1)
	unregister := service.RegisterForeground(
		func(current syscall.Signal) error {
			received <- current
			return nil
		},
	)
	defer unregister()

	select {
	case current := <-received:
		if current != syscall.SIGTERM {
			t.Fatalf("transferred signal = %s, want terminated", current)
		}
	case <-time.After(time.Second):
		t.Fatal("pending startup signal was not transferred")
	}
}

func TestAgentServiceGraceControlsCleanupDeadline(t *testing.T) {
	service := newTestService()
	const grace = 500 * time.Millisecond
	before := time.Now()
	service.RequestAgentStop(grace)

	ctx, cancel := service.CleanupContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline")
	}
	if remaining := deadline.Sub(before); remaining < 400*time.Millisecond ||
		remaining > 700*time.Millisecond {
		t.Fatalf("cleanup deadline = %s after request", remaining)
	}
}

func TestAgentServiceStopInterruptsForegroundBeforeCancellation(t *testing.T) {
	service := newTestService()
	received := make(chan syscall.Signal, 1)
	unregister := service.RegisterForeground(
		func(current syscall.Signal) error {
			received <- current
			return nil
		},
	)
	defer unregister()

	grace := ClientSandboxReserve + 50*time.Millisecond
	service.RequestAgentStop(grace)

	select {
	case current := <-received:
		if current != syscall.SIGINT {
			t.Fatalf("agent shutdown signal = %s, want interrupt", current)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground did not receive agent shutdown signal")
	}
	select {
	case <-service.Context().Done():
		t.Fatal("foreground context canceled before its interrupt grace")
	default:
	}
	select {
	case <-service.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("foreground context did not reach its bounded deadline")
	}
}

func TestAgentServiceShutdownBudget(t *testing.T) {
	if AgentClientGrace != 20*time.Second {
		t.Fatalf(
			"agent client grace = %s, want 20s",
			AgentClientGrace,
		)
	}
	advertised := AgentClientGrace - AgentClientMargin
	if advertised != 17*time.Second {
		t.Fatalf("advertised client grace = %s, want 17s", advertised)
	}
	interruptGrace := advertised - ClientSandboxReserve
	if interruptGrace != 12*time.Second {
		t.Fatalf("sandbox interrupt grace = %s, want 12s", interruptGrace)
	}
}
