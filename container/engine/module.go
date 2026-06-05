package engine

// fx wiring and constructors for the engine Service. Module registers the
// lifecycle-managed Service; New builds one without lifecycle hooks (for tests).

import (
	"context"
	"os"

	"go.uber.org/fx"
)

// Module registers the container Service in the fx graph.
func Module() fx.Option {
	return fx.Module("engine", fx.Provide(NewService))
}

// New constructs a Service without registering any lifecycle hook. It does not
// touch Docker; the client is created lazily on first use so it is safe to
// construct without a running daemon (used by tests).
func New() *Service {
	// Toby owns container teardown via terminateAll; disable testcontainers'
	// Ryuk reaper so no extra container is started (and host-network/Podman
	// setups are not disrupted).
	if _, ok := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !ok {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}

	return &Service{records: map[string]*record{}}
}

// NewService constructs the Service and registers an fx OnStop hook that
// terminates every still-tracked container.
func NewService(lc fx.Lifecycle) *Service {
	s := New()
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			s.terminateAll(ctx)
			return nil
		},
	})
	return s
}
