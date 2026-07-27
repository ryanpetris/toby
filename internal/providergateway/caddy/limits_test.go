//go:build linux

package caddy

// Verifies request, response, and total operation bounds.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadBoundsConfigurationBeforeDial(t *testing.T) {
	t.Parallel()

	var dialed atomic.Bool
	client, err := New(
		func(context.Context) (net.Conn, error) {
			dialed.Store(true)
			return nil, errors.New("unexpected dial")
		},
		Options{MaxConfigBodyBytes: 4},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	err = client.Load(t.Context(), []byte("12345"))
	if !errors.Is(err, ErrConfigTooLarge) {
		t.Fatalf("Load error = %v, want ErrConfigTooLarge", err)
	}
	if dialed.Load() {
		t.Error("oversized configuration reached the connector")
	}
}

func TestClientBoundsResponseBody(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "12345")
	})
	client := newAdminTestClient(
		t,
		handler,
		Options{MaxResponseBodyBytes: 4},
	)

	err := client.Load(t.Context(), []byte("{}"))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Load error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientClassifiesStatusBeforeResponseBodyLimit(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		call    func(*Client) error
		wantErr error
	}{
		{
			name:   "rejected load",
			status: http.StatusBadRequest,
			call: func(client *Client) error {
				return client.Load(t.Context(), []byte("{}"))
			},
			wantErr: ErrRejected,
		},
		{
			name:   "unavailable stop",
			status: http.StatusInternalServerError,
			call: func(client *Client) error {
				return client.Stop(t.Context())
			},
			wantErr: ErrUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(
					writer,
					"response larger than the configured limit",
				)
			})
			client := newAdminTestClient(
				t,
				handler,
				Options{MaxResponseBodyBytes: 4},
			)

			if err := test.call(client); !errors.Is(
				err,
				test.wantErr,
			) {
				t.Fatalf(
					"operation error = %v, want %v",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestProbeValidatesBoundedBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		limit   int64
		wantErr error
	}{
		{
			name:  "persist disabled",
			body:  " false\n",
			limit: 7,
		},
		{
			name:    "persist enabled",
			body:    "true",
			limit:   4,
			wantErr: ErrProtocol,
		},
		{
			name:    "trailing value",
			body:    "false null",
			limit:   10,
			wantErr: ErrProtocol,
		},
		{
			name:    "oversized",
			body:    "false",
			limit:   4,
			wantErr: ErrResponseTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, test.body)
				},
			)
			client := newAdminTestClient(
				t,
				handler,
				Options{MaxResponseBodyBytes: test.limit},
			)

			err := client.Probe(t.Context())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Probe error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestClientTimeoutsHeaderAndBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		writeHeader bool
	}{
		{name: "response header"},
		{name: "response body", writeHeader: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{})
			release := make(chan struct{})
			handler := http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					if test.writeHeader {
						w.Header().Set("Content-Length", "5")
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
					}
					close(started)
					<-release
				},
			)
			client := newAdminTestClient(
				t,
				handler,
				Options{RequestTimeout: 25 * time.Millisecond},
			)

			result := make(chan error, 1)
			go func() {
				result <- client.Probe(t.Context())
			}()
			<-started

			var err error
			select {
			case err = <-result:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("Probe did not honor its total timeout")
			}
			close(release)
			if !errors.Is(err, ErrRequestTimeout) {
				t.Fatalf("Probe error = %v, want ErrRequestTimeout", err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf(
					"Probe error = %v, want context deadline identity",
					err,
				)
			}
		})
	}
}

func TestClientMapsCallerCancellation(t *testing.T) {
	t.Parallel()

	var dialed atomic.Bool
	client, err := New(
		func(context.Context) (net.Conn, error) {
			dialed.Store(true)
			return nil, errors.New("unexpected dial")
		},
		Options{},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = client.Probe(ctx)
	if !errors.Is(err, ErrRequestCanceled) {
		t.Fatalf("Probe error = %v, want ErrRequestCanceled", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe error = %v, want context cancellation identity", err)
	}
	if dialed.Load() {
		t.Error("canceled request reached the connector")
	}
}
