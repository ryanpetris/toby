package server

// Adapts method-specific gRPC byte messages to resource-owned net.Conn streams.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

type byteServerConn struct {
	recv func() ([]byte, error)
	send func([]byte) error

	readMu  sync.Mutex
	readBuf []byte
	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

var _ net.Conn = (*byteServerConn)(nil)

func (c *byteServerConn) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	for len(c.readBuf) == 0 {
		if c.isClosed() {
			return 0, net.ErrClosed
		}
		data, err := c.recv()
		if err != nil {
			return 0, err
		}
		if len(data) == 0 {
			continue
		}
		c.readBuf = data
	}

	count := copy(destination, c.readBuf)
	c.readBuf = c.readBuf[count:]
	return count, nil
}

func (c *byteServerConn) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.isClosed() {
		return 0, net.ErrClosed
	}

	written := 0
	for written < len(data) {
		end := min(written+protocol.MaxStreamDataBytes, len(data))
		if err := c.send(data[written:end]); err != nil {
			return written, err
		}
		written = end
	}

	return written, nil
}

func (c *byteServerConn) Close() error {
	c.closeMu.Lock()
	c.closed = true
	c.closeMu.Unlock()

	return nil
}

func (c *byteServerConn) LocalAddr() net.Addr {
	return agentAddr("agent")
}

func (c *byteServerConn) RemoteAddr() net.Addr {
	return agentAddr("client")
}

func (*byteServerConn) SetDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (*byteServerConn) SetReadDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (*byteServerConn) SetWriteDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (c *byteServerConn) isClosed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	return c.closed
}

type agentAddr string

var _ net.Addr = agentAddr("")

func (a agentAddr) Network() string { return "grpc" }
func (a agentAddr) String() string  { return string(a) }

// ConnectMCP opens one lease-authorized MCP byte stream.
func (s *Service) ConnectMCP(
	stream agentv1.AgentService_ConnectMCPServer,
) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return invalidRequest(
			first.GetCorrelationId(),
			"MCP stream open must be the first message",
		)
	}

	correlationID := first.GetCorrelationId()
	return s.connectBytes(
		stream.Context(),
		protocol.ResourceMCP,
		correlationID,
		open.GetSessionId(),
		open.GetResourceId(),
		open.GetLeaseId(),
		func() ([]byte, error) {
			message, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			if message.GetCorrelationId() != correlationID {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"MCP stream correlation ID changed",
				)
			}
			if _, ok := message.GetValue().(*agentv1.MCPConnectRequest_Data); !ok {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"MCP stream received another open message",
				)
			}
			if len(message.GetData()) > protocol.MaxStreamDataBytes {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"MCP stream data message is too large",
				)
			}

			return append([]byte(nil), message.GetData()...), nil
		},
		func(data []byte) error {
			return stream.Send(&agentv1.MCPConnectResponse{
				CorrelationId: correlationID,
				Value: &agentv1.MCPConnectResponse_Data{
					Data: data,
				},
			})
		},
		func() error {
			return stream.Send(&agentv1.MCPConnectResponse{
				CorrelationId: correlationID,
				Value: &agentv1.MCPConnectResponse_Ready{
					Ready: &agentv1.MCPConnectReady{},
				},
			})
		},
	)
}

// ConnectModels opens one lease-authorized models byte stream.
func (s *Service) ConnectModels(
	stream agentv1.AgentService_ConnectModelsServer,
) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return invalidRequest(
			first.GetCorrelationId(),
			"models stream open must be the first message",
		)
	}

	correlationID := first.GetCorrelationId()
	return s.connectBytes(
		stream.Context(),
		protocol.ResourceModels,
		correlationID,
		open.GetSessionId(),
		open.GetResourceId(),
		open.GetLeaseId(),
		func() ([]byte, error) {
			message, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			if message.GetCorrelationId() != correlationID {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"models stream correlation ID changed",
				)
			}
			if _, ok := message.GetValue().(*agentv1.ModelsConnectRequest_Data); !ok {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"models stream received another open message",
				)
			}
			if len(message.GetData()) > protocol.MaxStreamDataBytes {
				return nil, invalidRequest(
					message.GetCorrelationId(),
					"models stream data message is too large",
				)
			}

			return append([]byte(nil), message.GetData()...), nil
		},
		func(data []byte) error {
			return stream.Send(&agentv1.ModelsConnectResponse{
				CorrelationId: correlationID,
				Value: &agentv1.ModelsConnectResponse_Data{
					Data: data,
				},
			})
		},
		func() error {
			return stream.Send(&agentv1.ModelsConnectResponse{
				CorrelationId: correlationID,
				Value: &agentv1.ModelsConnectResponse_Ready{
					Ready: &agentv1.ModelsConnectReady{},
				},
			})
		},
	)
}

func (s *Service) connectBytes(
	ctx context.Context,
	kind protocol.ResourceKind,
	correlationValue string,
	sessionValue string,
	resourceValue string,
	leaseValue string,
	recv func() ([]byte, error),
	send func([]byte) error,
	ready func() error,
) error {
	session, correlationID, err := s.requestSession(
		sessionValue,
		correlationValue,
	)
	if err != nil {
		return err
	}
	defer session.finish(correlationID)

	resourceID, leaseID, err := requireLease(
		session,
		resourceValue,
		leaseValue,
		correlationValue,
	)
	if err != nil {
		return err
	}

	s.beginStream()
	defer s.finishStream()

	resourceStream, err := s.resourceCoordinator.OpenResource(
		ctx,
		kind,
		resourceID,
		leaseID,
	)
	if err != nil {
		return agentError(
			correlationValue,
			protocol.ErrorUnavailable,
			fmt.Sprintf("%s resource stream is unavailable", kind),
			true,
		)
	}
	defer func() {
		s.options.Logger.DebugError(
			"close resource byte stream",
			resourceStream.Close(),
			"resource_kind", kind,
			"resource_id", resourceID,
		)
	}()

	byteStream, ok := resourceStream.(ByteResourceStream)
	if !ok {
		return agentError(
			correlationValue,
			protocol.ErrorInternal,
			"resource does not support byte streaming",
			false,
		)
	}
	if !session.ownsLease(resourceID, leaseID) {
		return agentError(
			correlationValue,
			protocol.ErrorLeaseNotFound,
			"resource lease was released while opening the stream",
			false,
		)
	}
	if err := ready(); err != nil {
		return err
	}

	connection := &byteServerConn{
		recv: recv,
		send: send,
	}
	defer func() {
		s.options.Logger.DebugError(
			"close resource byte connection",
			connection.Close(),
			"resource_kind", kind,
			"resource_id", resourceID,
		)
	}()

	err = byteStream.Serve(ctx, connection)
	if err == nil ||
		errors.Is(err, io.EOF) ||
		ctx.Err() != nil {
		return nil
	}

	return agentError(
		correlationValue,
		protocol.ErrorUnavailable,
		"resource byte stream failed",
		true,
	)
}
