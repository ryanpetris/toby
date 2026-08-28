// Package approval decides whether an action may proceed. It applies the configured
// permission rule, yolo mode, and the built-in defaults for an action identified by
// its RPC method name; when the policy says to ask, the action is denied.
package approval

import (
	"context"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/permission"
)

// Request describes an action awaiting a decision. Default is the caller's policy when
// nothing is configured for the action — the service that owns the action supplies it,
// so there is no central list of actions or defaults.
type Request struct {
	Action  string
	Name    string
	Message string
	Default permission.Rule
}

// Service resolves approval decisions against the host config.
type Service struct {
	config *appconfig.LaunchHolder
	logger *diagnostic.Logger
}

// New constructs the launch approval service.
func New(
	config *appconfig.LaunchHolder,
	diagnostics *diagnostic.Service,
) *Service {
	return &Service{
		config: config,
		logger: diagnostics.Logger("approval"),
	}
}

// Request resolves the decision for an action. A policy outcome is never an error.
func (s *Service) Request(_ context.Context, req Request) (permission.Decision, error) {
	config := s.config.Current()
	rule := config.PermissionRule(req.Action)
	yolo := config.Settings().YoloEnabled()

	decision := permission.Resolve(rule, req.Default, yolo)
	s.logger.Debug(
		"resolved approval request",
		"action",
		req.Action,
		"configured_rule",
		rule,
		"default_rule",
		req.Default,
		"yolo",
		yolo,
		"decision",
		decision,
	)

	return decision, nil
}
