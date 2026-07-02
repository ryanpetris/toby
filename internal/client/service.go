// Package client is the thin Toby CLI side of the daemon channel. It connects to
// (or spawns) the daemon over the injected transport, issues control requests, and
// — for a launch — runs the foreground tool itself. This file covers dialing and the
// daemon.* helpers; session start and the foreground exec are added alongside the
// project registry.
package client

import (
	"context"
	"encoding/json"
	"errors"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
	"petris.dev/toby/internal/daemon/transport"
	"petris.dev/toby/internal/version"
)

// ErrDaemonNotRunning is returned by connect-only helpers when no daemon is reachable.
var ErrDaemonNotRunning = errors.New("toby daemon is not running")

// Service dials the daemon and issues client->daemon requests. It also owns the local
// Docker client the foreground exec runs through — the client runs the tool itself so
// its interactive PTY attaches straight to the user's terminal.
type Service struct {
	bootstrap transport.Bootstrap
	connector transport.Connector
	engine    *engine.Service
}

func newService(bootstrap transport.Bootstrap, connector transport.Connector) *Service {
	return &Service{bootstrap: bootstrap, connector: connector, engine: engine.New()}
}

// Endpoint reports the transport endpoint for messages.
func (s *Service) Endpoint() string { return s.connector.Endpoint() }

// dial connects to a running daemon, spawning one first when spawn is true. handler
// dispatches daemon->client callbacks (approval prompts); it is nil for the plain
// daemon.* helpers that expect no callbacks.
func (s *Service) dial(ctx context.Context, spawn bool, handler control.Handler) (*control.Peer, error) {
	if spawn {
		if err := s.bootstrap.EnsureDaemon(ctx); err != nil {
			return nil, err
		}
	}
	conn, err := s.connector.Connect(ctx)
	if err != nil {
		if !spawn {
			return nil, ErrDaemonNotRunning
		}
		return nil, err
	}
	peer := control.NewPeer(ctx, conn, handler)
	peer.Start(nil)
	return peer, nil
}

// Ping starts the daemon if needed and returns its identity.
func (s *Service) Ping(ctx context.Context) (protocol.PingResult, error) {
	peer, err := s.dial(ctx, true, nil)
	if err != nil {
		return protocol.PingResult{}, err
	}
	defer peer.Close()
	return call[protocol.PingResult](ctx, peer, protocol.MethodDaemonPing, protocol.PingParams{Version: version.String()})
}

// Status returns daemon and project state, without spawning a daemon.
func (s *Service) Status(ctx context.Context) (protocol.StatusResult, error) {
	peer, err := s.dial(ctx, false, nil)
	if err != nil {
		return protocol.StatusResult{}, err
	}
	defer peer.Close()
	return call[protocol.StatusResult](ctx, peer, protocol.MethodDaemonStatus, nil)
}

// Stop asks a running daemon to shut down; it does not spawn one.
func (s *Service) Stop(ctx context.Context) error {
	peer, err := s.dial(ctx, false, nil)
	if err != nil {
		return err
	}
	defer peer.Close()
	_, err = call[struct{}](ctx, peer, protocol.MethodDaemonStop, nil)
	return err
}

// StopProject tears down one project's container, returning how many were stopped. It
// does not spawn a daemon.
func (s *Service) StopProject(ctx context.Context, label string) (int, error) {
	peer, err := s.dial(ctx, false, nil)
	if err != nil {
		return 0, err
	}
	defer peer.Close()
	result, err := call[protocol.ProjectStopResult](ctx, peer, protocol.MethodProjectStop, protocol.ProjectStopParams{Label: label})
	if err != nil {
		return 0, err
	}
	return result.Stopped, nil
}

// call issues a request and decodes the typed result from the JSON-RPC response.
func call[T any](ctx context.Context, peer *control.Peer, method string, params any) (T, error) {
	var out T
	resp, err := peer.Call(ctx, method, params)
	if err != nil {
		return out, err
	}
	if resp.Result == nil {
		return out, nil
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}
