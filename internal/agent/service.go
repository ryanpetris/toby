package agent

// Runs the elected agent listener and guarantees coordinator shutdown after
// all agent sessions have drained.

import (
	"context"
	"errors"
	"fmt"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/resourcelease"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/shutdown"
	"petris.dev/toby/internal/version"
)

// ErrAlreadyRunning reports that another per-user agent owns the socket.
var ErrAlreadyRunning = errors.New("per-user agent is already running")

// ServeOptions controls the lifetime policy for one agent invocation.
type ServeOptions struct {
	Persistent bool
}

// Service owns one invocation of `tobyd`.
type Service struct {
	paths     config.Paths
	server    *agentserver.Service
	resources *resourcelease.Service
	warnings  *warning.Service
	logger    *diagnostic.Logger
}

// NewService constructs the agent process service.
func NewService(
	paths config.Paths,
	server *agentserver.Service,
	resources *resourcelease.Service,
	warnings *warning.Service,
	diagnostics *diagnostic.Service,
) *Service {
	return &Service{
		paths:     paths,
		server:    server,
		resources: resources,
		warnings:  warnings,
		logger:    diagnostics.Logger("agent.service"),
	}
}

// Serve adopts or elects the per-user socket, serves the listener, and treats
// an existing protocol-compatible agent as an already-running result.
func (s *Service) Serve(
	ctx context.Context,
	options ServeOptions,
) (returnErr error) {
	if s == nil ||
		s.server == nil ||
		s.resources == nil {
		return fmt.Errorf("agent is not configured")
	}
	if ctx == nil {
		return fmt.Errorf("agent context is nil")
	}

	runtimePaths, err := s.paths.ResolveRuntime()
	if err != nil {
		return err
	}

	activatedListener, activated, err := socket.SystemdListener(
		runtimePaths.AgentSocket,
		socket.Options{Logger: s.logger},
	)
	if err != nil {
		return err
	}
	if activated {
		defer s.shutdownResources()

		return s.server.Serve(
			ctx,
			activatedListener,
			agentserver.ServeOptions{
				Persistent: options.Persistent,
			},
		)
	}

	election, err := socket.Elect(
		ctx,
		runtimePaths.AgentSocket,
		socket.Options{Logger: s.logger},
	)
	if err != nil {
		return err
	}
	if election.Conn != nil {
		session, err := agentclient.OpenAgent(
			ctx,
			election.Conn,
			version.String(),
			agentclient.Options{},
			s.warnings,
			nil,
		)
		if err != nil {
			return fmt.Errorf(
				"an incompatible or unhealthy agent owns %s: %w; stop that service before starting a protocol-compatible binary",
				runtimePaths.AgentSocket,
				err,
			)
		}
		s.logger.DebugError(
			"close agent compatibility probe session",
			session.Close(),
		)
		return ErrAlreadyRunning
	}
	if election.Listener == nil {
		return fmt.Errorf("agent election returned no endpoint")
	}

	defer func() {
		s.shutdownResources()
	}()

	return s.server.Serve(
		ctx,
		election.Listener,
		agentserver.ServeOptions{Persistent: options.Persistent},
	)
}

func (s *Service) shutdownResources() {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		shutdown.AgentResourceGrace,
	)
	defer cancel()

	s.logger.DebugError(
		"shut down agent resources",
		s.resources.Shutdown(ctx),
	)
}
