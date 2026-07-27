//go:build linux

package caddy

// Exercises the OCI Caddy image, read-only Bubblewrap service, protected
// sockets, native load, data traffic, and exact teardown on an opted-in host.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/providergateway"
)

func TestCaddyImageRunsInBubblewrap(t *testing.T) {
	if os.Getenv("TOBY_CADDY_OCI_INTEGRATION") != "1" {
		t.Skip(
			"set TOBY_CADDY_OCI_INTEGRATION=1 on a Linux host with Bubblewrap",
		)
	}

	base, err := os.MkdirTemp(
		".",
		".tc-",
	)
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeParent := os.Getenv("XDG_RUNTIME_DIR")
	runtimeRoot, err := os.MkdirTemp(
		runtimeParent,
		"tc-",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runtimeRoot); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(runtimeRoot, "auth.sock")
	election, err := socket.Elect(
		t.Context(),
		authPath,
		socket.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("authorization socket election lost")
	}
	t.Cleanup(func() {
		if err := election.Listener.Close(); err != nil {
			t.Error(err)
		}
	})

	builder, err := resource.NewBuilder(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	image := integrationImage(t)
	pool, err := NewPool(
		config.Paths{
			XDGCacheHome:  filepath.Join(base, "cache"),
			XDGDataHome:   filepath.Join(base, "data"),
			XDGRuntimeDir: runtimeRoot,
		},
		builder,
		image,
		authPath,
		election.Listener.File,
		PoolOptions{
			IdleTimeout:      time.Second,
			ReadinessTimeout: 20 * time.Second,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			20*time.Second,
		)
		defer cancel()
		if err := pool.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})

	generation, err := pool.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(generation.Release)

	data, err := json.Marshal(map[string]any{
		"admin": map[string]any{
			"listen": unixAddress(defaultAdminSocket, "0600"),
			"config": map[string]any{"persist": false},
		},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{
					"writer": map[string]any{"output": "discard"},
				},
			},
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"test": map[string]any{
						"listen": []string{
							unixAddress(defaultDataSocket, "0600"),
						},
						"routes": []any{
							map[string]any{
								"handle": []any{
									map[string]any{
										"handler":     "static_response",
										"status_code": 204,
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := generation.Load(t.Context(), data); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(
				ctx context.Context,
				_, _ string,
			) (net.Conn, error) {
				return generation.DialData(ctx)
			},
		},
	}
	assertDataStatus(t, client, http.StatusNoContent)

	err = generation.Load(
		t.Context(),
		[]byte(
			`{"apps":{"http":{"servers":{"invalid":{"routes":[{"handle":[{"handler":"unregistered_handler"}]}]}}}}`,
		),
	)
	if !errors.Is(
		err,
		providergateway.ErrConfigurationRejected,
	) {
		t.Fatalf(
			"invalid load error = %v, want configuration rejected",
			err,
		)
	}

	assertDataStatus(t, client, http.StatusNoContent)
}

func assertDataStatus(
	t *testing.T,
	client *http.Client,
	want int,
) {
	t.Helper()

	response, err := client.Get("http://caddy-data.invalid/")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf(
			"data response cleanup = %v, %v",
			readErr,
			closeErr,
		)
	}
	if response.StatusCode != want {
		t.Fatalf(
			"data status = %d, want %d",
			response.StatusCode,
			want,
		)
	}
}
