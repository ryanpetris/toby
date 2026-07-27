package resourcehandler

// Adapts one acquired MCP backend connection to an agent resource stream.

import (
	"context"
	"fmt"
	"net"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
)

type stream struct {
	service  *Service
	id       protocol.ResourceID
	upstream connector.Target
	once     sync.Once
}

var _ agentserver.ResourceStream = (*stream)(nil)

func newStream(
	service *Service,
	id protocol.ResourceID,
	acquired mcpgateway.AcquiredBackend,
) (*stream, error) {
	upstream := acquired.Target()
	if upstream == nil {
		return nil, fmt.Errorf("MCP backend target is unavailable")
	}

	return &stream{
		service:  service,
		id:       id,
		upstream: upstream,
	}, nil
}

func (s *stream) Serve(
	ctx context.Context,
	connection net.Conn,
) (returnErr error) {
	defer func() {
		s.service.logger.DebugError(
			"close MCP resource stream",
			s.Close(),
			"resource_id",
			s.id,
		)
	}()
	s.upstream.ServeConnector(ctx, connection)

	return nil
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
