package mount

// fx wiring and constructors for the mount Service.

import "go.uber.org/fx"

// New constructs a Service without registering any lifecycle hook.
func New() *Service { return &Service{} }

// NewService constructs the Service for the fx graph. The lifecycle is accepted
// for house-style symmetry with the other container services; the mount service
// holds no resources that require teardown.
func NewService(lc fx.Lifecycle) *Service {
	_ = lc
	return New()
}

// Module registers the mount Service in the fx graph.
func Module() fx.Option {
	return fx.Module("mount", fx.Provide(NewService))
}
