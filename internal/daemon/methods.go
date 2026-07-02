// The daemon.* control capability: ping (liveness + version), status (daemon and
// project state), and stop (graceful shutdown). It is a control.Capability so it
// plugs into the same router the client<->daemon channel dispatches through. Project
// state is empty until the project registry is wired in.

package daemon

import (
	"context"
	"os"
	"time"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"

	"go.uber.org/fx"
)

var _ control.Capability = (*methods)(nil)

// projectLister reports the sanitized state of live projects for daemon.status. The
// project registry satisfies it once wired; until then a nil lister reports none.
type projectLister interface {
	StatusList() []protocol.ProjectStatus
}

type methods struct {
	shutdowner fx.Shutdowner
	options    Options
	version    string
	startedAt  time.Time
	projects   projectLister
}

func newMethods(shutdowner fx.Shutdowner, options Options, version string, startedAt time.Time, projects projectLister) *methods {
	return &methods{shutdowner: shutdowner, options: options, version: version, startedAt: startedAt, projects: projects}
}

func (m *methods) Methods() []control.Method {
	return []control.Method{
		{Name: protocol.MethodDaemonPing, Handle: m.handlePing},
		{Name: protocol.MethodDaemonStatus, Handle: m.handleStatus},
		{Name: protocol.MethodDaemonStop, Handle: m.handleStop},
	}
}

func (m *methods) handlePing(_ context.Context, req control.RPCRequest) ([]byte, error) {
	return control.ResponseOK(req.ID, protocol.PingResult{Version: m.version, PID: os.Getpid()}), nil
}

func (m *methods) handleStatus(_ context.Context, req control.RPCRequest) ([]byte, error) {
	result := protocol.StatusResult{
		Version:  m.version,
		PID:      os.Getpid(),
		Uptime:   time.Since(m.startedAt).Round(time.Second).String(),
		Projects: m.projectStatuses(),
	}
	return control.ResponseOK(req.ID, result), nil
}

func (m *methods) handleStop(_ context.Context, req control.RPCRequest) ([]byte, error) {
	// Reply before shutting down so the client sees success rather than a dropped
	// connection; the shutdown is fired after the response is written.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = m.shutdowner.Shutdown()
	}()
	return control.ResponseOK(req.ID, struct{}{}), nil
}

func (m *methods) projectStatuses() []protocol.ProjectStatus {
	if m.projects == nil {
		return nil
	}
	return m.projects.StatusList()
}
