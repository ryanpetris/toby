package mcpserver

// Per-session context: SessionState is the non-secret, host-side state one launch
// hands to the MCP server (paths, options, and the live sandbox/proxy/config/
// registry handles); Session is the live execution context shared across a
// session's tool and resource calls and the lock that serializes host operations.

import (
	"sync"

	"petris.dev/toby/config"
	"petris.dev/toby/internal/approval"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/mcpproxy"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

// Session is the per-session execution context shared across a session's tool and
// resource calls. Service packages read Git, State, and Resources, and use
// Serialize to run host operations (notably Git) without interleaving.
type Session struct {
	Git       GitClient
	State     SessionState
	Resources []Resource
	mu        sync.Mutex
}

// Serialize runs fn while holding the session lock, so concurrent tool calls do
// not interleave host operations within one session.
func (s *Session) Serialize(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

type SessionState struct {
	Debug       bool
	Paths       config.Paths
	Options     tools.Options
	Sandbox     *sandbox.SandboxService
	MCPProxy    *mcpproxy.Service
	Approval    *approval.Service
	Config      *appconfig.Service
	Registry    *tools.Registry
	ActiveTools []string
	PrimaryTool string
}

func (s SessionState) Clone() SessionState {
	s.ActiveTools = append([]string(nil), s.ActiveTools...)
	return s
}
