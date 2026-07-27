package sandboxgateway

// Implements the typed sandbox resource service over one gRPC connection.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"petris.dev/toby/internal/diagnostic"
	sandboxv1 "petris.dev/toby/internal/gen/toby/sandbox/v1"
	sandboxprotocol "petris.dev/toby/internal/sandboxgateway/protocol"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type service struct {
	sandboxv1.UnimplementedSandboxServiceServer

	openers       map[string]Opener
	permits       chan struct{}
	binaryVersion string
	logger        *diagnostic.Logger
}

var _ sandboxv1.SandboxServiceServer = (*service)(nil)

func newService(
	openers map[string]Opener,
	maxConnections int,
	binaryVersion string,
	logger *diagnostic.Logger,
) *service {
	return &service{
		openers:       openers,
		permits:       make(chan struct{}, maxConnections),
		binaryVersion: binaryVersion,
		logger:        logger,
	}
}

// Hello reports the run-scoped endpoint protocol before a client opens a
// resource stream.
func (s *service) Hello(
	_ context.Context,
	request *sandboxv1.HelloRequest,
) (*sandboxv1.HelloResponse, error) {
	if request == nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"hello request is required",
		)
	}
	if err := validateIdentifier(
		"correlation ID",
		request.GetCorrelationId(),
	); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	return &sandboxv1.HelloResponse{
		CorrelationId:   request.GetCorrelationId(),
		BinaryVersion:   s.binaryVersion,
		ProtocolVersion: sandboxprotocol.Version,
	}, nil
}

func (s *service) ConnectResource(
	stream sandboxv1.SandboxService_ConnectResourceServer,
) error {
	select {
	case s.permits <- struct{}{}:
		defer func() {
			<-s.permits
		}()
	default:
		return status.Error(
			codes.ResourceExhausted,
			"sandbox resource connection limit reached",
		)
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	correlationID := first.GetCorrelationId()
	if err := validateIdentifier(
		"correlation ID",
		correlationID,
	); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(
			codes.InvalidArgument,
			"resource open must be the first message",
		)
	}
	resourceID := open.GetResourceId()
	if err := validateIdentifier(
		"client resource ID",
		resourceID,
	); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	opener := s.openers[resourceID]
	if opener == nil {
		return status.Error(
			codes.NotFound,
			"resource is not registered for this sandbox",
		)
	}
	resource, err := opener.OpenResource(stream.Context())
	if err != nil {
		return status.Error(
			codes.Unavailable,
			"resource is unavailable",
		)
	}
	if resource == nil {
		return status.Error(
			codes.Internal,
			"resource opener returned no stream",
		)
	}
	if err := stream.Send(&sandboxv1.ResourceConnectResponse{
		CorrelationId: correlationID,
		Value: &sandboxv1.ResourceConnectResponse_Ready{
			Ready: &sandboxv1.ResourceConnectReady{},
		},
	}); err != nil {
		s.logger.DebugError(
			"close unopened sandbox resource",
			resource.Close(),
			"resource_id", resourceID,
		)
		return err
	}

	return relayResource(
		stream.Context(),
		stream,
		correlationID,
		resource,
		s.logger,
	)
}

func relayResource(
	ctx context.Context,
	stream sandboxv1.SandboxService_ConnectResourceServer,
	correlationID string,
	resource io.ReadWriteCloser,
	logger *diagnostic.Logger,
) error {
	var closeOnce sync.Once
	var closeErr error
	closeResource := func() {
		closeOnce.Do(func() {
			closeErr = resource.Close()
		})
	}

	inputDone := make(chan error, 1)
	go func() {
		inputDone <- receiveResourceInput(
			stream,
			correlationID,
			resource,
		)
	}()

	outputDone := make(chan error, 1)
	go func() {
		outputDone <- sendResourceOutput(
			stream,
			correlationID,
			resource,
		)
	}()

	stop := context.AfterFunc(ctx, closeResource)
	defer stop()

	select {
	case inputErr := <-inputDone:
		if inputErr != nil {
			closeResource()
		}
		outputErr := <-outputDone
		closeResource()
		logger.DebugError(
			"close sandbox resource stream",
			closeErr,
			"correlation_id", correlationID,
		)
		return resourceStreamResult(ctx, inputErr, outputErr)
	case outputErr := <-outputDone:
		closeResource()
		logger.DebugError(
			"close sandbox resource stream",
			closeErr,
			"correlation_id", correlationID,
		)
		return resourceStreamResult(ctx, outputErr, nil)
	case <-ctx.Done():
		closeResource()
		<-inputDone
		<-outputDone
		logger.DebugError(
			"close sandbox resource stream",
			closeErr,
			"correlation_id", correlationID,
		)
		return ctx.Err()
	}
}

func receiveResourceInput(
	stream sandboxv1.SandboxService_ConnectResourceServer,
	correlationID string,
	resource io.Writer,
) error {
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if closer, ok := resource.(interface {
				CloseWrite() error
			}); ok {
				return closer.CloseWrite()
			}
			return nil
		}
		if err != nil {
			return err
		}
		if message.GetCorrelationId() != correlationID {
			return status.Error(
				codes.InvalidArgument,
				"resource stream correlation ID changed",
			)
		}
		if _, ok := message.GetValue().(*sandboxv1.ResourceConnectRequest_Data); !ok {
			return status.Error(
				codes.InvalidArgument,
				"resource stream received another open message",
			)
		}
		data := message.GetData()
		if len(data) > maxStreamDataBytes {
			return status.Error(
				codes.InvalidArgument,
				"resource stream data message is too large",
			)
		}
		if err := writeAll(resource, data); err != nil {
			return err
		}
	}
}

func sendResourceOutput(
	stream sandboxv1.SandboxService_ConnectResourceServer,
	correlationID string,
	resource io.Reader,
) error {
	buffer := make([]byte, maxStreamDataBytes)
	for {
		count, err := resource.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			if sendErr := stream.Send(
				&sandboxv1.ResourceConnectResponse{
					CorrelationId: correlationID,
					Value: &sandboxv1.ResourceConnectResponse_Data{
						Data: data,
					},
				},
			); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
}

func resourceStreamResult(
	ctx context.Context,
	first error,
	second error,
) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}

	result := errors.Join(
		normalizeStreamError(first),
		normalizeStreamError(second),
	)
	if result == nil {
		return nil
	}
	if first != nil && status.Code(first) != codes.Unknown {
		return first
	}
	if second != nil && status.Code(second) != codes.Unknown {
		return second
	}

	return status.Error(
		codes.Unavailable,
		"resource stream failed",
	)
}

func normalizeStreamError(err error) error {
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}

	return nil
}

func validateResourceService(
	openers map[string]Opener,
	options Options,
) (map[string]Opener, Options, error) {
	allowlist, err := cloneOpeners(openers)
	if err != nil {
		return nil, Options{}, err
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, Options{}, err
	}
	if len(allowlist) > maximumConnections*16 {
		return nil, Options{}, fmt.Errorf(
			"sandbox gateway resource registry is too large",
		)
	}

	return allowlist, normalized, nil
}
