package sandboxgateway

// Connects one sandbox process's stdio to a registered resource over gRPC.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic"
	sandboxv1 "petris.dev/toby/internal/gen/toby/sandbox/v1"
	sandboxprotocol "petris.dev/toby/internal/sandboxgateway/protocol"
	"petris.dev/toby/internal/uuid"
	"petris.dev/toby/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Connect bridges stdin and stdout to one sandbox resource.
func Connect(
	ctx context.Context,
	path string,
	resourceID string,
	stdin io.ReadCloser,
	stdout io.Writer,
) error {
	if ctx == nil {
		return fmt.Errorf(
			"sandbox resource connector context must not be nil",
		)
	}
	if err := validateIdentifier(
		"client resource ID",
		resourceID,
	); err != nil {
		return err
	}
	if isNil(stdin) {
		return fmt.Errorf(
			"sandbox resource connector stdin must not be nil",
		)
	}
	if isNil(stdout) {
		return fmt.Errorf(
			"sandbox resource connector stdout must not be nil",
		)
	}

	connection, err := grpc.NewClient(
		"passthrough:///toby-sandbox",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMessageBytes),
			grpc.MaxCallSendMsgSize(maxMessageBytes),
		),
		grpc.WithContextDialer(func(
			dialContext context.Context,
			_ string,
		) (net.Conn, error) {
			return socket.Dial(
				dialContext,
				path,
				socket.Options{},
			)
		}),
	)
	if err != nil {
		return fmt.Errorf("create sandbox gRPC client: %w", err)
	}
	defer func() {
		diagnostic.DiscardError(
			"the sandbox resource connector owns foreground streams",
			"close sandbox gRPC client connection",
			connection.Close(),
			"socket_path", path,
		)
	}()

	client := sandboxv1.NewSandboxServiceClient(connection)
	if err := negotiateProtocol(ctx, client); err != nil {
		return err
	}

	streamContext, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	stream, err := client.ConnectResource(streamContext)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("connect sandbox resource: %w", err)
	}
	correlationID, err := uuid.NewV4()
	if err != nil {
		return err
	}
	if err := stream.Send(&sandboxv1.ResourceConnectRequest{
		CorrelationId: correlationID,
		Value: &sandboxv1.ResourceConnectRequest_Open{
			Open: &sandboxv1.ResourceConnectOpen{
				ResourceId: resourceID,
			},
		},
	}); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("open sandbox resource: %w", err)
	}
	ready, err := stream.Recv()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("open sandbox resource: %w", err)
	}
	if ready.GetCorrelationId() != correlationID ||
		ready.GetReady() == nil {
		return fmt.Errorf(
			"sandbox resource returned an invalid readiness response",
		)
	}

	return copyClientStreams(
		ctx,
		cancelStream,
		stream,
		correlationID,
		stdin,
		stdout,
	)
}

func negotiateProtocol(
	ctx context.Context,
	client sandboxv1.SandboxServiceClient,
) error {
	correlationID, err := uuid.NewV4()
	if err != nil {
		return err
	}
	response, err := client.Hello(ctx, &sandboxv1.HelloRequest{
		CorrelationId: correlationID,
		BinaryVersion: version.String(),
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("request sandbox hello: %w", err)
	}
	return validateHelloResponse(correlationID, response)
}

func validateHelloResponse(
	correlationID string,
	response *sandboxv1.HelloResponse,
) error {
	if response == nil {
		return fmt.Errorf("sandbox hello returned no response")
	}
	if response.GetCorrelationId() != correlationID {
		return fmt.Errorf(
			"sandbox hello correlation ID %q does not match %q",
			response.GetCorrelationId(),
			correlationID,
		)
	}
	if response.GetBinaryVersion() == "" {
		return fmt.Errorf("sandbox hello omitted the binary version")
	}
	if !sandboxprotocol.SupportsVersion(response.GetProtocolVersion()) {
		return sandboxprotocol.VersionError{
			Received:  response.GetProtocolVersion(),
			Supported: sandboxprotocol.SupportedVersions(),
		}
	}

	return nil
}

func copyClientStreams(
	ctx context.Context,
	cancel context.CancelFunc,
	stream sandboxv1.SandboxService_ConnectResourceClient,
	correlationID string,
	stdin io.ReadCloser,
	stdout io.Writer,
) error {
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- sendClientInput(
			stream,
			correlationID,
			stdin,
		)
	}()

	outputDone := make(chan error, 1)
	go func() {
		outputDone <- receiveClientOutput(
			stream,
			correlationID,
			stdout,
		)
	}()

	select {
	case inputErr := <-inputDone:
		if inputErr != nil {
			cancel()
			closeErr := stdin.Close()
			outputErr := <-outputDone
			diagnostic.DiscardError(
				"the sandbox resource stream result is already determined",
				"close sandbox resource input",
				closeErr,
				"correlation_id", correlationID,
			)
			return errors.Join(
				clientStreamResult(ctx, inputErr),
				normalizeClientShutdown(outputErr),
			)
		}
		outputErr := <-outputDone
		return clientStreamResult(ctx, outputErr)
	case outputErr := <-outputDone:
		cancel()
		closeErr := stdin.Close()
		inputErr := <-inputDone
		diagnostic.DiscardError(
			"the sandbox resource stream result is already determined",
			"close sandbox resource input",
			normalizeClientShutdown(closeErr),
			"correlation_id", correlationID,
		)
		return errors.Join(
			clientStreamResult(ctx, outputErr),
			normalizeClientShutdown(inputErr),
		)
	case <-ctx.Done():
		cancel()
		closeErr := stdin.Close()
		<-inputDone
		<-outputDone
		diagnostic.DiscardError(
			"the sandbox resource stream was canceled",
			"close sandbox resource input",
			normalizeClientShutdown(closeErr),
			"correlation_id", correlationID,
		)
		return ctx.Err()
	}
}

func sendClientInput(
	stream sandboxv1.SandboxService_ConnectResourceClient,
	correlationID string,
	stdin io.Reader,
) error {
	buffer := make([]byte, maxStreamDataBytes)
	for {
		count, err := stdin.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			if sendErr := stream.Send(
				&sandboxv1.ResourceConnectRequest{
					CorrelationId: correlationID,
					Value: &sandboxv1.ResourceConnectRequest_Data{
						Data: data,
					},
				},
			); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			return stream.CloseSend()
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
}

func receiveClientOutput(
	stream sandboxv1.SandboxService_ConnectResourceClient,
	correlationID string,
	stdout io.Writer,
) error {
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.GetCorrelationId() != correlationID {
			return fmt.Errorf(
				"sandbox resource correlation ID changed",
			)
		}
		if _, ok := message.GetValue().(*sandboxv1.ResourceConnectResponse_Data); !ok {
			return fmt.Errorf(
				"sandbox resource returned another readiness response",
			)
		}
		if err := writeAll(stdout, message.GetData()); err != nil {
			return err
		}
	}
}

func clientStreamResult(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}

	return fmt.Errorf("sandbox resource stream: %w", err)
}

func normalizeClientShutdown(err error) error {
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) ||
		status.Code(err) == codes.Canceled {
		return nil
	}

	return err
}
