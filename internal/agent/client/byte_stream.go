package client

// Adapts method-specific gRPC byte streams to launch-side net.Conn users.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

type byteClientConn struct {
	cancel    context.CancelFunc
	recv      func() ([]byte, error)
	send      func([]byte) error
	closeSend func() error

	readMu  sync.Mutex
	readBuf []byte
	writeMu sync.Mutex
	closeMu sync.Mutex

	writeClosed bool
	closed      bool
}

var _ net.Conn = (*byteClientConn)(nil)

func (c *byteClientConn) Read(destination []byte) (int, error) {
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

func (c *byteClientConn) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.closeMu.Lock()
	closed := c.closed || c.writeClosed
	c.closeMu.Unlock()
	if closed {
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

// CloseWrite half-closes the client-to-agent side of the gRPC stream.
func (c *byteClientConn) CloseWrite() error {
	c.closeMu.Lock()
	if c.closed || c.writeClosed {
		c.closeMu.Unlock()
		return nil
	}
	c.writeClosed = true
	c.closeMu.Unlock()

	return c.closeSend()
}

func (c *byteClientConn) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	writeClosed := c.writeClosed
	c.writeClosed = true
	c.closeMu.Unlock()

	var closeSendErr error
	if !writeClosed {
		closeSendErr = c.closeSend()
	}
	c.cancel()

	return closeSendErr
}

func (*byteClientConn) LocalAddr() net.Addr {
	return clientAgentAddr("client")
}

func (*byteClientConn) RemoteAddr() net.Addr {
	return clientAgentAddr("agent")
}

func (*byteClientConn) SetDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (*byteClientConn) SetReadDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (*byteClientConn) SetWriteDeadline(time.Time) error {
	return errors.ErrUnsupported
}

func (c *byteClientConn) isClosed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	return c.closed
}

type clientAgentAddr string

var _ net.Addr = clientAgentAddr("")

func (a clientAgentAddr) Network() string { return "grpc" }
func (a clientAgentAddr) String() string  { return string(a) }

// OpenResourceStream opens one method-specific MCP or models byte stream.
func (s *AgentSession) OpenResourceStream(
	ctx context.Context,
	kind protocol.ResourceKind,
	lease *ResourceLease,
) (net.Conn, error) {
	if s == nil {
		return nil, fmt.Errorf("agent session is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("agent stream context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lease == nil || lease.session != s {
		return nil, fmt.Errorf(
			"resource lease does not belong to this agent session",
		)
	}

	switch kind {
	case protocol.ResourceMCP:
		return s.openMCPStream(ctx, lease)
	case protocol.ResourceModels:
		return s.openModelsStream(ctx, lease)
	default:
		return nil, fmt.Errorf(
			"resource kind %q does not provide a byte stream",
			kind,
		)
	}
}

func (s *AgentSession) openMCPStream(
	ctx context.Context,
	lease *ResourceLease,
) (net.Conn, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := s.client.ConnectMCP(streamCtx)
	if err != nil {
		cancel()
		return nil, remoteError(err)
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := stream.Send(&agentv1.MCPConnectRequest{
		CorrelationId: string(id),
		Value: &agentv1.MCPConnectRequest_Open{
			Open: &agentv1.MCPConnectOpen{
				SessionId:  string(s.sessionID),
				ResourceId: string(lease.resourceID),
				LeaseId:    string(lease.leaseID),
			},
		},
	}); err != nil {
		cancel()
		return nil, remoteRequestError(err, id)
	}
	ready, err := stream.Recv()
	if err != nil {
		cancel()
		return nil, remoteRequestError(err, id)
	}
	if err := requireCorrelation(ready.GetCorrelationId(), id); err != nil {
		cancel()
		return nil, err
	}
	if ready.GetReady() == nil {
		cancel()
		return nil, fmt.Errorf("agent MCP stream did not become ready")
	}

	return &byteClientConn{
		cancel: cancel,
		recv: func() ([]byte, error) {
			message, err := stream.Recv()
			if err != nil {
				return nil, remoteRequestError(err, id)
			}
			if err := requireCorrelation(
				message.GetCorrelationId(),
				id,
			); err != nil {
				return nil, err
			}
			if _, ok := message.GetValue().(*agentv1.MCPConnectResponse_Data); !ok {
				return nil, fmt.Errorf(
					"agent MCP stream returned a non-data message",
				)
			}
			if len(message.GetData()) > protocol.MaxStreamDataBytes {
				return nil, fmt.Errorf(
					"agent MCP stream returned an oversized data message",
				)
			}

			return append([]byte(nil), message.GetData()...), nil
		},
		send: func(data []byte) error {
			return stream.Send(&agentv1.MCPConnectRequest{
				CorrelationId: string(id),
				Value: &agentv1.MCPConnectRequest_Data{
					Data: data,
				},
			})
		},
		closeSend: stream.CloseSend,
	}, nil
}

func (s *AgentSession) openModelsStream(
	ctx context.Context,
	lease *ResourceLease,
) (net.Conn, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := s.client.ConnectModels(streamCtx)
	if err != nil {
		cancel()
		return nil, remoteError(err)
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := stream.Send(&agentv1.ModelsConnectRequest{
		CorrelationId: string(id),
		Value: &agentv1.ModelsConnectRequest_Open{
			Open: &agentv1.ModelsConnectOpen{
				SessionId:  string(s.sessionID),
				ResourceId: string(lease.resourceID),
				LeaseId:    string(lease.leaseID),
			},
		},
	}); err != nil {
		cancel()
		return nil, remoteRequestError(err, id)
	}
	ready, err := stream.Recv()
	if err != nil {
		cancel()
		return nil, remoteRequestError(err, id)
	}
	if err := requireCorrelation(ready.GetCorrelationId(), id); err != nil {
		cancel()
		return nil, err
	}
	if ready.GetReady() == nil {
		cancel()
		return nil, fmt.Errorf(
			"agent models stream did not become ready",
		)
	}

	return &byteClientConn{
		cancel: cancel,
		recv: func() ([]byte, error) {
			message, err := stream.Recv()
			if err != nil {
				return nil, remoteRequestError(err, id)
			}
			if err := requireCorrelation(
				message.GetCorrelationId(),
				id,
			); err != nil {
				return nil, err
			}
			if _, ok := message.GetValue().(*agentv1.ModelsConnectResponse_Data); !ok {
				return nil, fmt.Errorf(
					"agent models stream returned a non-data message",
				)
			}
			if len(message.GetData()) > protocol.MaxStreamDataBytes {
				return nil, fmt.Errorf(
					"agent models stream returned an oversized data message",
				)
			}

			return append([]byte(nil), message.GetData()...), nil
		},
		send: func(data []byte) error {
			return stream.Send(&agentv1.ModelsConnectRequest{
				CorrelationId: string(id),
				Value: &agentv1.ModelsConnectRequest_Data{
					Data: data,
				},
			})
		},
		closeSend: stream.CloseSend,
	}, nil
}
