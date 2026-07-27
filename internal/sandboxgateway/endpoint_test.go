package sandboxgateway

// Exercises typed resource selection, byte streaming, validation, and stream
// cancellation over the real private Unix endpoint.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/socket"
	sandboxv1 "petris.dev/toby/internal/gen/toby/sandbox/v1"
	sandboxprotocol "petris.dev/toby/internal/sandboxgateway/protocol"
	"petris.dev/toby/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestEndpointHelloReportsSandboxProtocol(t *testing.T) {
	oldVersion := version.Current
	version.Current = "v0.9.0-test"
	t.Cleanup(func() {
		version.Current = oldVersion
	})

	endpoint := listenTestEndpoint(t, map[string]Opener{
		"resource": blockingOpener(t),
	})
	client, closeClient := newTestClient(t, endpoint.Path())
	defer closeClient()

	response, err := client.Hello(
		t.Context(),
		&sandboxv1.HelloRequest{
			CorrelationId: "correlation",
			BinaryVersion: "third-party-client",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetCorrelationId() != "correlation" {
		t.Fatalf(
			"hello correlation ID = %q, want correlation",
			response.GetCorrelationId(),
		)
	}
	if response.GetBinaryVersion() != "v0.9.0-test" {
		t.Fatalf(
			"hello binary version = %q, want v0.9.0-test",
			response.GetBinaryVersion(),
		)
	}
	if response.GetProtocolVersion() != sandboxprotocol.Version {
		t.Fatalf(
			"hello protocol version = %d, want %d",
			response.GetProtocolVersion(),
			sandboxprotocol.Version,
		)
	}
}

func TestEndpointHelloRejectsInvalidCorrelationID(t *testing.T) {
	endpoint := listenTestEndpoint(t, map[string]Opener{
		"resource": blockingOpener(t),
	})
	client, closeClient := newTestClient(t, endpoint.Path())
	defer closeClient()

	_, err := client.Hello(
		t.Context(),
		&sandboxv1.HelloRequest{},
	)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Hello() error = %v, want InvalidArgument", err)
	}
}

func TestEndpointSelectsResourceAndPreservesBytes(t *testing.T) {
	input := []byte{0, 1, 2, '\n', 0xff}
	endpoint := listenTestEndpoint(t, map[string]Opener{
		"first":  replyOpener(t, input, []byte("first:")),
		"second": replyOpener(t, input, []byte("second:")),
	})

	for _, test := range []struct {
		id     string
		prefix string
	}{
		{id: "first", prefix: "first:"},
		{id: "second", prefix: "second:"},
	} {
		t.Run(test.id, func(t *testing.T) {
			var output bytes.Buffer
			err := Connect(
				t.Context(),
				endpoint.Path(),
				test.id,
				io.NopCloser(bytes.NewReader(input)),
				&output,
			)
			if err != nil {
				t.Fatal(err)
			}

			want := append([]byte(test.prefix), input...)
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatalf(
					"output = %q, want %q",
					output.Bytes(),
					want,
				)
			}
		})
	}
}

func TestEndpointRejectsUnknownResource(t *testing.T) {
	endpoint := listenTestEndpoint(t, map[string]Opener{
		"known": replyOpener(t, nil, nil),
	})

	err := Connect(
		t.Context(),
		endpoint.Path(),
		"unknown",
		io.NopCloser(strings.NewReader("")),
		io.Discard,
	)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Connect() error = %v, want NotFound", err)
	}
}

func TestEndpointRejectsMessagesAfterOpenWithoutLosingStatus(
	t *testing.T,
) {
	for _, test := range []struct {
		name    string
		request *sandboxv1.ResourceConnectRequest
	}{
		{
			name: "changed correlation",
			request: &sandboxv1.ResourceConnectRequest{
				CorrelationId: "different",
				Value: &sandboxv1.ResourceConnectRequest_Data{
					Data: []byte("payload"),
				},
			},
		},
		{
			name: "second open",
			request: &sandboxv1.ResourceConnectRequest{
				CorrelationId: "correlation",
				Value: &sandboxv1.ResourceConnectRequest_Open{
					Open: &sandboxv1.ResourceConnectOpen{
						ResourceId: "resource",
					},
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := listenTestEndpoint(t, map[string]Opener{
				"resource": blockingOpener(t),
			})
			client, closeClient := newTestClient(
				t,
				endpoint.Path(),
			)
			defer closeClient()

			stream, err := client.ConnectResource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = stream.Send(&sandboxv1.ResourceConnectRequest{
				CorrelationId: "correlation",
				Value: &sandboxv1.ResourceConnectRequest_Open{
					Open: &sandboxv1.ResourceConnectOpen{
						ResourceId: "resource",
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(test.request); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); status.Code(err) !=
				codes.InvalidArgument {
				t.Fatalf(
					"Recv() error = %v, want InvalidArgument",
					err,
				)
			}
		})
	}
}

func TestEndpointCancellationClosesResource(t *testing.T) {
	resourceOpened := make(chan struct{})
	resourceClosed := make(chan struct{})
	endpoint := listenTestEndpoint(t, map[string]Opener{
		"resource": OpenFunc(func(
			context.Context,
		) (io.ReadWriteCloser, error) {
			gateway, backend := net.Pipe()
			close(resourceOpened)
			go func() {
				defer close(resourceClosed)
				defer backend.Close()

				var buffer [1]byte
				_, _ = backend.Read(buffer[:])
			}()

			return gateway, nil
		}),
	})

	ctx, cancel := context.WithCancel(t.Context())
	input, inputWriter := io.Pipe()
	connectDone := make(chan error, 1)
	go func() {
		connectDone <- Connect(
			ctx,
			endpoint.Path(),
			"resource",
			input,
			io.Discard,
		)
	}()

	select {
	case <-resourceOpened:
	case <-time.After(time.Second):
		t.Fatal("Connect() did not open the resource")
	}
	cancel()
	_ = inputWriter.Close()

	select {
	case err := <-connectDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"Connect() error = %v, want context cancellation",
				err,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("Connect() did not stop after cancellation")
	}
	select {
	case <-resourceClosed:
	case <-time.After(time.Second):
		t.Fatal("resource remained open after cancellation")
	}
}

func TestEndpointCloseRemovesTransientSocketWithoutElectionState(
	t *testing.T,
) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sandbox.sock")
	endpoint, err := Listen(path, map[string]Opener{
		"resource": blockingOpener(t),
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after Close() error = %v, want not found", err)
	}
	lockPath := filepath.Join(directory, ".sandbox.sock.lock")
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(
			"election state after Close() error = %v, want not found",
			err,
		)
	}
}

func listenTestEndpoint(
	t *testing.T,
	openers map[string]Opener,
) *Endpoint {
	t.Helper()

	endpoint, err := Listen(
		filepath.Join(t.TempDir(), "sandbox.sock"),
		openers,
		Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := endpoint.Close(); err != nil {
			t.Error(err)
		}
	})

	return endpoint
}

func replyOpener(
	t *testing.T,
	request []byte,
	prefix []byte,
) Opener {
	t.Helper()

	return OpenFunc(func(
		context.Context,
	) (io.ReadWriteCloser, error) {
		gateway, backend := net.Pipe()
		go func() {
			defer backend.Close()

			payload := make([]byte, len(request))
			if _, err := io.ReadFull(backend, payload); err != nil {
				t.Errorf("read resource request: %v", err)
				return
			}
			response := append(append([]byte(nil), prefix...), payload...)
			if _, err := backend.Write(response); err != nil {
				t.Errorf("write resource response: %v", err)
			}
		}()

		return gateway, nil
	})
}

func blockingOpener(t *testing.T) Opener {
	t.Helper()

	return OpenFunc(func(
		context.Context,
	) (io.ReadWriteCloser, error) {
		gateway, backend := net.Pipe()
		go func() {
			defer backend.Close()
			_, _ = io.Copy(io.Discard, backend)
		}()

		return gateway, nil
	})
}

func newTestClient(
	t *testing.T,
	path string,
) (sandboxv1.SandboxServiceClient, func()) {
	t.Helper()

	connection, err := grpc.NewClient(
		"passthrough:///toby-sandbox-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithContextDialer(func(
			ctx context.Context,
			_ string,
		) (net.Conn, error) {
			return socket.Dial(ctx, path, socket.Options{})
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	return sandboxv1.NewSandboxServiceClient(connection), func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	}
}
