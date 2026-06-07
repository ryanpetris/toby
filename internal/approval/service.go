// Package approval decides whether an action may proceed. It applies the configured
// permission rule, yolo mode, and the built-in defaults, and — when the policy says to
// ask — prompts the user through the active interactive terminal. Any host-side caller
// can request a decision for an action identified by its RPC method name.
package approval

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/permission"
	"petris.dev/toby/sandbox"
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

// PrompterSource yields the approval prompter for the active interactive session, or
// nil when there is none — no host terminal, or no foreground tool running.
type PrompterSource interface {
	ApprovalPrompter() sandbox.ApprovalPrompter
}

// Service resolves approval decisions against the host config and the active prompter.
type Service struct {
	config *appconfig.Service

	mu     sync.RWMutex
	source PrompterSource
}

func New(config *appconfig.Service) *Service {
	return &Service{config: config}
}

// SetPrompterSource installs the source of the active terminal prompter (the sandbox
// service). It is set once per session before any tool runs.
func (s *Service) SetPrompterSource(source PrompterSource) {
	s.mu.Lock()
	s.source = source
	s.mu.Unlock()
}

func (s *Service) prompter() sandbox.ApprovalPrompter {
	s.mu.RLock()
	source := s.source
	s.mu.RUnlock()
	if source == nil {
		return nil
	}
	return source.ApprovalPrompter()
}

// Request resolves the decision for an action, prompting the user when the policy
// requires it. It returns an error only when the prompt itself fails (e.g. a cancelled
// context); a policy outcome is never an error.
func (s *Service) Request(ctx context.Context, req Request) (permission.Decision, error) {
	rule := s.config.PermissionRule(req.Action)
	yolo := s.config.YoloEnabled()

	prompter := s.prompter()
	// The managed terminal registers the prompter; when it's off (or there is no
	// interactive terminal) there is no prompter, so an ask becomes a deny.
	canAsk := prompter != nil

	decision, mustAsk := permission.Resolve(rule, req.Default, yolo, canAsk)
	tracef("request action=%s rule=%v default=%v yolo=%v prompter=%v -> decision=%v mustAsk=%v",
		req.Action, rule, req.Default, yolo, prompter != nil, decision, mustAsk)
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

// tracef appends a diagnostic line to TOBY_FG_LOG (the same file the foreground traces
// to), so the approval path lines up with the modal lifecycle. It is a no-op when the
// env var is unset.
func tracef(format string, args ...any) {
	path := os.Getenv("TOBY_FG_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s approval: "+format+"\n", append([]any{time.Now().Format("15:04:05.000000")}, args...)...)
}
