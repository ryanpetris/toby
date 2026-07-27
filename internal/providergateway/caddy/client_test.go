package caddy

// Verifies client construction, option bounds, closure, and redacted display.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestNewRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	connector := func(context.Context) (net.Conn, error) {
		return nil, errors.New("unused")
	}
	tests := []struct {
		name      string
		connector Connector
		options   Options
	}{
		{
			name: "nil connector",
		},
		{
			name:      "negative timeout",
			connector: connector,
			options: Options{
				RequestTimeout: -time.Nanosecond,
			},
		},
		{
			name:      "excessive timeout",
			connector: connector,
			options: Options{
				RequestTimeout: maximumRequestTimeout + time.Nanosecond,
			},
		},
		{
			name:      "negative config limit",
			connector: connector,
			options: Options{
				MaxConfigBodyBytes: -1,
			},
		},
		{
			name:      "excessive config limit",
			connector: connector,
			options: Options{
				MaxConfigBodyBytes: maximumConfigBodyBytes + 1,
			},
		},
		{
			name:      "negative response limit",
			connector: connector,
			options: Options{
				MaxResponseBodyBytes: -1,
			},
		},
		{
			name:      "excessive response limit",
			connector: connector,
			options: Options{
				MaxResponseBodyBytes: maximumResponseBodyBytes + 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, err := New(test.connector, test.options)
			if client != nil {
				t.Error("invalid options returned a client")
			}
			if !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("New error = %v, want ErrInvalidOptions", err)
			}
		})
	}
}

func TestClientClosePreventsOperations(t *testing.T) {
	t.Parallel()

	client, err := New(
		func(context.Context) (net.Conn, error) {
			return nil, errors.New("unused protected socket")
		},
		Options{},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	if got := client.String(); got != "{CaddyAdmin:<redacted>}" {
		t.Fatalf("String() = %q", got)
	}
	if err := client.Close(); err != nil {
		t.Fatal("close client:", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal("close client twice:", err)
	}
	if err := client.Probe(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Probe after Close error = %v, want ErrUnavailable", err)
	}
}

func TestClientRejectsNonUnixConnector(t *testing.T) {
	t.Parallel()

	client, err := New(
		func(context.Context) (net.Conn, error) {
			connection, peer := net.Pipe()
			t.Cleanup(func() {
				_ = peer.Close()
			})
			return connection, nil
		},
		Options{},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	err = client.Probe(t.Context())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Probe error = %v, want ErrUnavailable", err)
	}
	assertRedactedError(t, err, adminOrigin, configPath)
}

func TestClientRejectsNilContexts(t *testing.T) {
	t.Parallel()

	client, err := New(
		func(context.Context) (net.Conn, error) {
			return nil, errors.New("unused")
		},
		Options{},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	var nilContext context.Context
	for name, call := range map[string]func() error{
		"load": func() error {
			return client.Load(nilContext, []byte("{}"))
		},
		"probe": func() error {
			return client.Probe(nilContext)
		},
		"stop": func() error {
			return client.Stop(nilContext)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := call(); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("operation error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
