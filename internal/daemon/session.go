// The session.* control capability. session.start acquires the project's netns unit
// (once, shared across launches) and the profile's shared home container, runs the
// tool lifecycle against the home manager (streaming install output back to the
// client), creates a tool container joined to the netns, and returns its id for the
// client to attach to. session.release terminates the tool container and drops both
// holds so the idle timers can arm. Live sessions are tracked by id.

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"petris.dev/toby/container/engine"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/configwatch"
	"petris.dev/toby/internal/daemon/home"
	"petris.dev/toby/internal/daemon/project"
	"petris.dev/toby/internal/daemon/protocol"
	"petris.dev/toby/internal/session/run"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

var _ control.Capability = (*sessionMethods)(nil)

// liveSession is a client's active hold: the netns registry session, the shared home
// lease, the netns unit, and the per-invocation tool container.
type liveSession struct {
	netns     *project.Session
	home      *home.Lease
	container *run.Container
	toolID    string
	sid       string
}

type sessionMethods struct {
	registry *project.Registry
	homeReg  *home.Registry
	engine   *engine.Service
	watcher  *configwatch.Watcher

	mu       sync.Mutex
	sessions map[string]*liveSession
	counter  atomic.Uint64
}

func newSessionMethods(registry *project.Registry, homeReg *home.Registry, eng *engine.Service, watcher *configwatch.Watcher) *sessionMethods {
	return &sessionMethods{registry: registry, homeReg: homeReg, engine: eng, watcher: watcher, sessions: map[string]*liveSession{}}
}

func (s *sessionMethods) Methods() []control.Method {
	return []control.Method{
		{Name: protocol.MethodSessionStart, Handle: s.handleStart},
		{Name: protocol.MethodSessionRelease, Handle: s.handleRelease},
		{Name: protocol.MethodProjectStop, Handle: s.handleProjectStop},
	}
}

func (s *sessionMethods) handleProjectStop(_ context.Context, req control.RPCRequest) ([]byte, error) {
	var params protocol.ProjectStopParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), nil
	}
	stopped := s.registry.StopByLabel(params.Label)
	return control.ResponseOK(req.ID, protocol.ProjectStopResult{Stopped: stopped}), nil
}

func (s *sessionMethods) handleStart(ctx context.Context, req control.RPCRequest) ([]byte, error) {
	var params protocol.SessionStartParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), nil
	}

	opts, overrides, err := decodeLaunch(params)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), nil
	}
	profile := s.watcher.Current().WithOverrides(overrides).HomeProfile()

	key := projectKey(opts, profile)
	bring := &bringUpRequest{options: &opts, overrides: overrides, requestedTools: params.RequestedTools, primary: params.Primary, profile: profile}
	netns, err := s.registry.Acquire(ctx, key, bring)
	if err != nil {
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), nil
	}
	handle, ok := netns.Handle().(*projectHandle)
	if !ok {
		netns.Release()
		return control.ResponseError(req.ID, control.CodeInternalError, "unexpected project handle", nil), nil
	}
	container := handle.Container()

	lease, err := s.homeReg.Acquire(ctx, profile, container.Image(), container.BinVolume())
	if err != nil {
		netns.Release()
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), nil
	}
	binding := run.HomeBinding{Client: lease.Client(), BaseEnv: lease.BaseEnv(), UID: lease.UID(), GID: lease.GID()}

	sid := fmt.Sprintf("s-%d", s.counter.Add(1))
	sink := s.installSink(ctx, sid)

	if params.Install {
		lease.InstallLock().Lock()
		installErr := container.Install(ctx, binding, false, sink)
		lease.InstallLock().Unlock()
		lease.Release()
		netns.Release()
		if installErr != nil {
			return control.ResponseError(req.ID, control.CodeInternalError, installErr.Error(), nil), nil
		}
		return control.ResponseOK(req.ID, protocol.SessionStartResult{InstallOnly: true}), nil
	}

	lease.InstallLock().Lock()
	toolID, managed, err := container.PreLaunch(ctx, binding, sid, params.Extra, params.Upgrade, sink)
	lease.InstallLock().Unlock()
	if err != nil {
		lease.Release()
		netns.Release()
		return control.ResponseError(req.ID, control.CodeInternalError, err.Error(), nil), nil
	}

	s.store(sid, &liveSession{netns: netns, home: lease, container: container, toolID: toolID, sid: sid})
	// Route this session's approval prompts back to the client that started it, for the
	// lifetime of its tool run (cleared on release).
	if peer := peerFrom(ctx); peer != nil {
		container.SetApprovalPrompter(newPeerPrompter(peer, sid))
	}
	return control.ResponseOK(req.ID, protocol.SessionStartResult{SessionID: sid, ContainerID: toolID, Managed: managed}), nil
}

func (s *sessionMethods) handleRelease(ctx context.Context, req control.RPCRequest) ([]byte, error) {
	var params protocol.SessionReleaseParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return control.ResponseError(req.ID, control.CodeInvalidParams, err.Error(), nil), nil
	}
	s.mu.Lock()
	live := s.sessions[params.SessionID]
	delete(s.sessions, params.SessionID)
	s.mu.Unlock()
	if live != nil {
		live.container.SetApprovalPrompter(nil)
		if live.toolID != "" {
			_ = s.engine.RemoveByID(ctx, live.toolID)
		}
		live.container.ReleaseSession(ctx, live.home.Client(), live.sid)
		live.home.Release()
		live.netns.Release()
	}
	return control.ResponseOK(req.ID, struct{}{}), nil
}

// installSink forwards streamed install/exec output to the client that started the
// session as install.output notifications.
func (s *sessionMethods) installSink(ctx context.Context, sid string) sandbox.InstallSink {
	peer := peerFrom(ctx)
	if peer == nil {
		return nil
	}
	return func(stream string, data []byte) {
		_ = peer.Notify(protocol.MethodInstallOutput, protocol.InstallOutputParams{SessionID: sid, Stream: stream, Data: data})
	}
}

func (s *sessionMethods) store(sid string, live *liveSession) {
	s.mu.Lock()
	s.sessions[sid] = live
	s.mu.Unlock()
}

// decodeLaunch decodes the opaque options/overrides and folds install/upgrade in.
func decodeLaunch(params protocol.SessionStartParams) (tools.Options, appconfig.LaunchOverrides, error) {
	var opts tools.Options
	if len(params.Options) > 0 {
		if err := json.Unmarshal(params.Options, &opts); err != nil {
			return tools.Options{}, appconfig.LaunchOverrides{}, err
		}
	}
	var overrides appconfig.LaunchOverrides
	if len(params.Overrides) > 0 {
		if err := json.Unmarshal(params.Overrides, &overrides); err != nil {
			return tools.Options{}, appconfig.LaunchOverrides{}, err
		}
	}
	opts.Install = params.Install
	opts.Upgrade = params.Upgrade
	return opts, overrides, nil
}

// projectKey derives the netns registry key from a launch's environment, project
// sources, and home profile (so different profiles get distinct netns containers).
func projectKey(opts tools.Options, profile string) project.Key {
	label := opts.Env
	if label == "" && len(opts.Projects) > 0 {
		label = opts.Projects[0].Name
	}
	paths := make([]string, 0, len(opts.Projects))
	for _, p := range opts.Projects {
		paths = append(paths, p.Source)
	}
	return project.NewKey(label, profile, paths)
}
