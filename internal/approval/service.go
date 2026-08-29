// Package approval decides whether an action may proceed. It applies the
// configured permission rule, yolo mode, and the built-in defaults for an
// action identified by its RPC method name. When the policy says to ask, the
// request is held as a pending approval record; the user decides it
// out-of-band with `toby approvals`, and approving executes the held request
// under a one-shot in-process grant.
package approval

import (
	"context"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/permission"
)

// Service resolves authorization against the host config and the launch's
// approval records.
type Service struct {
	config   *appconfig.LaunchHolder
	logger   *diagnostic.Logger
	registry *Registry
}

// New constructs the launch approval service and its registry.
func New(
	config *appconfig.LaunchHolder,
	diagnostics *diagnostic.Service,
) *Service {
	return &Service{
		config:   config,
		logger:   diagnostics.Logger("approval"),
		registry: NewRegistry(),
	}
}

// Registry returns the launch's approval records.
func (s *Service) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

// Authorize resolves whether the request may run now. It returns nil to run,
// ErrDenied to refuse, or a PendingError naming the approval record the user
// must decide first. A context grant redeems an approved record's single
// execution instead of resolving policy.
func (s *Service) Authorize(ctx context.Context, req Request) error {
	if id, granted := grantFrom(ctx); granted {
		err := s.registry.RedeemGrant(id, req.Action)
		s.logger.Debug(
			"redeemed approval grant",
			"action", req.Action,
			"approval", id,
			"redeem_error", err,
		)
		return err
	}

	config := s.config.Current()
	rule := config.PermissionRule(req.Action)
	yolo := config.Settings().YoloEnabled()

	outcome := permission.Resolve(rule, req.Default, yolo)
	s.logger.Debug(
		"resolved approval request",
		"action", req.Action,
		"configured_rule", rule,
		"default_rule", req.Default,
		"yolo", yolo,
		"outcome", outcome,
	)
	switch outcome {
	case permission.Allow:
		return nil
	case permission.Deny:
		return ErrDenied
	}

	created, err := s.registry.Create(req)
	if err != nil {
		return err
	}
	return &PendingError{
		ID:      created.ID,
		Name:    created.Name,
		Message: created.Message,
	}
}
