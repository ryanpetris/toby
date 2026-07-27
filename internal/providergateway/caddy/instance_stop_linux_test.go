//go:build linux

package caddy

// Verifies that graceful-stop fallback observes final process state instead of
// reporting an administration race after a clean reap.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"petris.dev/toby/internal/sandbox/bwrap"
)

type stopTestBackground struct {
	done chan struct{}
	stop func() error
}

var _ bwrap.BackgroundProcess = (*stopTestBackground)(nil)

func (b *stopTestBackground) Done() <-chan struct{} {
	return b.done
}

func (*stopTestBackground) Err() error {
	return nil
}

func (b *stopTestBackground) Stop(context.Context) error {
	return b.stop()
}

func (*stopTestBackground) Kill(context.Context) error {
	return nil
}

func TestInstanceStopTreatsCleanFallbackReapAsSuccess(
	t *testing.T,
) {
	t.Parallel()

	admin := newAdminTestClient(
		t,
		http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writer.WriteHeader(http.StatusInternalServerError)
		}),
		Options{},
	)
	done := make(chan struct{})
	var closeOnce sync.Once
	background := &stopTestBackground{done: done}
	instance := &Instance{
		background: background,
		admin:      admin,
		done:       done,
	}
	background.stop = func() error {
		closeOnce.Do(func() {
			close(done)
		})
		return errors.New("signal raced clean process exit")
	}

	if err := instance.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v, want nil", err)
	}
}
