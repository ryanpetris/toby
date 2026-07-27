//go:build linux

package caddy

// Verifies that transport, request, and Caddy response details never escape
// through administration errors.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
)

func TestClientRedactsConnectorFailure(t *testing.T) {
	t.Parallel()

	const (
		secret     = "real-provider-secret"
		socketPath = "/run/user/1000/toby/caddy/secret/admin.sock"
	)
	client, err := New(
		func(context.Context) (net.Conn, error) {
			return nil, fmt.Errorf(
				"dial %s with Authorization: Bearer %s",
				socketPath,
				secret,
			)
		},
		Options{},
	)
	if err != nil {
		t.Fatal("construct client:", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	err = client.Load(
		t.Context(),
		[]byte(`{"credential":"`+secret+`"}`),
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Load error = %v, want ErrUnavailable", err)
	}
	assertRedactedError(
		t,
		err,
		secret,
		socketPath,
		"Authorization",
		loadPath,
		adminOrigin,
	)
}

func TestClientRedactsRejectedResponse(t *testing.T) {
	t.Parallel()

	const (
		secret  = "upstream-credential-secret"
		routeID = "route-capability-secret"
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Authorization", "Bearer "+secret)
		w.Header().Set("Location", "https://upstream.invalid/"+routeID)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(
			w,
			"Caddy rejected Authorization for "+secret+" on "+routeID,
		)
	})
	client := newAdminTestClient(t, handler, Options{})

	err := client.Load(
		t.Context(),
		[]byte(`{"route":"`+routeID+`","secret":"`+secret+`"}`),
	)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Load error = %v, want ErrRejected", err)
	}
	assertRedactedError(
		t,
		err,
		secret,
		routeID,
		"Authorization",
		"upstream.invalid",
		loadPath,
		adminOrigin,
	)
}
