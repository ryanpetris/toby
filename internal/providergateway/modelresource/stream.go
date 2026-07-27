package modelresource

// Adapts one acquired models backend connection to an agent resource stream.

import (
	"context"
	"net"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	agentserver "petris.dev/toby/internal/agent/server"
)

type stream struct {
	service *Service
	id      protocol.ResourceID
	backend modelsBackend
	once    sync.Once
}

var _ agentserver.ResourceStream = (*stream)(nil)

func (s *stream) Serve(
	ctx context.Context,
	connection net.Conn,
) (returnErr error) {
	defer func() {
		s.service.logger.DebugError(
			"close models resource stream",
			s.Close(),
			"resource_id",
			s.id,
		)
	}()
	return s.backend.Serve(ctx, connection)
}

func (s *stream) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.service.releaseStream(s.id)
	})
	return nil
}
