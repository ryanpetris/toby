// Package approval decides whether an action may proceed. It applies the configured
// permission rule, yolo mode, and the built-in defaults, and — when the policy says to
// ask — prompts the user through the active interactive terminal. Any host-side caller
// can request a decision for an action identified by its RPC method name.
package approval

import (
	"context"
	"sync"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/permission"
	"petris.dev/toby/internal/sandbox"
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

// Service resolves approval decisions against the host config and the active prompter.
type Service struct {
	config *appconfig.LaunchHolder
	logger *diagnostic.Logger

	mu       sync.RWMutex
	prompter sandbox.ApprovalPrompter
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

// SetPrompter installs or clears the prompter owned by the active foreground
// Bubblewrap execution.
func (s *Service) SetPrompter(prompter sandbox.ApprovalPrompter) {
	s.mu.Lock()
	s.prompter = prompter
	s.mu.Unlock()
}

func (s *Service) currentPrompter() sandbox.ApprovalPrompter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prompter
}

// Request resolves the decision for an action, prompting the user when the policy
// requires it. It returns an error only when the prompt itself fails (e.g. a cancelled
// context); a policy outcome is never an error.
func (s *Service) Request(ctx context.Context, req Request) (permission.Decision, error) {
	config := s.config.Current()
	rule := config.PermissionRule(req.Action)
	yolo := config.Settings().YoloEnabled()

	prompter := s.currentPrompter()
	// The managed terminal registers the prompter; when it's off (or there is no
	// interactive terminal) there is no prompter, so an ask becomes a deny.
	canAsk := prompter != nil

	decision, mustAsk := permission.Resolve(rule, req.Default, yolo, canAsk)
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
		"prompter_available",
		prompter != nil,
		"decision",
		decision,
		"must_ask",
		mustAsk,
	)
	if !mustAsk {
		return decision, nil
	}

	allow, err := prompter.PromptApproval(ctx, sandbox.ApprovalRequest{
		Action:  req.Action,
		Name:    req.Name,
		Message: req.Message,
	})
	if err != nil {
		return permission.Deny, err
	}
	if allow {
		return permission.Allow, nil
	}
	return permission.Deny, nil
}
