//go:build linux

package caddy

// Verifies the exact administration method, path, headers, and response
// contracts over a Unix socket.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

type observedAdminRequest struct {
	method      string
	path        string
	contentType string
	body        []byte
}

func TestClientOperations(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("NO_PROXY", "")

	requests := make(chan observedAdminRequest, 3)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- observedAdminRequest{
			method:      r.Method,
			path:        r.URL.RequestURI(),
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}

		w.WriteHeader(http.StatusOK)
		if r.URL.Path == configPath {
			_, _ = io.WriteString(w, "false\n")
		}
	})
	client := newAdminTestClient(t, handler, Options{})

	config := []byte(`{"admin":{"config":{"persist":false}}}`)
	if err := client.Load(t.Context(), config); err != nil {
		t.Fatal("Load:", err)
	}
	if err := client.Probe(t.Context()); err != nil {
		t.Fatal("Probe:", err)
	}
	if err := client.Stop(t.Context()); err != nil {
		t.Fatal("Stop:", err)
	}

	expected := []observedAdminRequest{
		{
			method:      http.MethodPost,
			path:        loadPath,
			contentType: "application/json",
			body:        config,
		},
		{
			method: http.MethodGet,
			path:   configPath,
		},
		{
			method: http.MethodPost,
			path:   stopPath,
		},
	}
	for index, want := range expected {
		got := <-requests
		if got.method != want.method ||
			got.path != want.path ||
			got.contentType != want.contentType ||
			!bytes.Equal(got.body, want.body) {
			t.Errorf("request %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestClientRejectsRedirect(t *testing.T) {
	t.Parallel()

	const secretLocation = "/route-capability-secret"
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != configPath {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", secretLocation)
		w.WriteHeader(http.StatusFound)
	})
	client := newAdminTestClient(t, handler, Options{})

	err := client.Probe(t.Context())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("Probe error = %v, want ErrProtocol", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	assertRedactedError(t, err, secretLocation, configPath, adminOrigin)
}

func TestClientRequiresDocumentedStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		call    func(*Client, context.Context) error
		wantErr error
	}{
		{
			name:   "probe no content",
			status: http.StatusNoContent,
			call: func(client *Client, ctx context.Context) error {
				return client.Probe(ctx)
			},
			wantErr: ErrProtocol,
		},
		{
			name:   "load created",
			status: http.StatusCreated,
			call: func(client *Client, ctx context.Context) error {
				return client.Load(ctx, []byte("{}"))
			},
			wantErr: ErrProtocol,
		},
		{
			name:   "load rejected",
			status: http.StatusBadRequest,
			call: func(client *Client, ctx context.Context) error {
				return client.Load(ctx, []byte("{}"))
			},
			wantErr: ErrRejected,
		},
		{
			name:   "stop server error",
			status: http.StatusInternalServerError,
			call: func(client *Client, ctx context.Context) error {
				return client.Stop(ctx)
			},
			wantErr: ErrUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(test.status)
				},
			)
			client := newAdminTestClient(t, handler, Options{})

			err := test.call(client, t.Context())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("operation error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestClientRequiresDocumentedSuccessBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(*Client, context.Context) error
	}{
		{
			name: "load",
			call: func(client *Client, ctx context.Context) error {
				return client.Load(ctx, []byte("{}"))
			},
		},
		{
			name: "stop",
			call: func(client *Client, ctx context.Context) error {
				return client.Stop(ctx)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "unexpected")
				},
			)
			client := newAdminTestClient(t, handler, Options{})

			err := test.call(client, t.Context())
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("operation error = %v, want ErrProtocol", err)
			}
		})
	}
}
