package providergateway

// Proves loopback-only binding, transparent duplex relay, generation
// publication, natural drain, and connector-driven teardown.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRelayConnector struct {
	done chan struct{}
	once sync.Once
}

var _ Connector = (*testRelayConnector)(nil)

func newTestRelayConnector() *testRelayConnector {
	return &testRelayConnector{done: make(chan struct{})}
}

func (c *testRelayConnector) Done() <-chan struct{} {
	return c.done
}

func (c *testRelayConnector) Close() {
	c.once.Do(func() {
		close(c.done)
	})
}

func TestRelayCopiesBytesAndRetainsGenerationConnector(t *testing.T) {
	socket, serverDone := startRelayTestServer(t, func(connection net.Conn) {
		defer connection.Close()

		body, err := io.ReadAll(connection)
		if err != nil {
			return
		}
		_, _ = connection.Write(
			[]byte(strings.ToUpper(string(body))),
		)
	})
	defer func() {
		<-serverDone
	}()

	gateway, err := newRelay(relayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	if !strings.HasPrefix(
		gateway.baseURL(),
		"http://127.0.0.1:",
	) {
		t.Fatalf("relay base URL = %q", gateway.baseURL())
	}

	connector := newTestRelayConnector()
	if err := gateway.publish(
		1,
		relayTestDialer(socket),
		func() (Connector, error) {
			return connector, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	connection := dialRelay(t, gateway)
	if _, err := connection.Write([]byte("provider bytes")); err != nil {
		t.Fatal(err)
	}
	if err := connection.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "PROVIDER BYTES" {
		t.Fatalf("relay response = %q", response)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-connector.Done():
	case <-time.After(time.Second):
		t.Fatal("relay did not release generation connector")
	}
}

func TestRelayUnpublishDrainsExistingAndRejectsNewConnections(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	socket, serverDone := startRelayTestServer(t, func(connection net.Conn) {
		defer connection.Close()

		line, err := bufio.NewReader(connection).ReadString('\n')
		if err != nil {
			return
		}
		<-release
		_, _ = io.WriteString(connection, "reply:"+line)
	})
	defer func() {
		releaseOnce.Do(func() {
			close(release)
		})
		<-serverDone
	}()

	gateway, err := newRelay(relayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	if err := gateway.publish(
		2,
		relayTestDialer(socket),
		func() (Connector, error) {
			return newTestRelayConnector(), nil
		},
	); err != nil {
		t.Fatal(err)
	}

	existing := dialRelay(t, gateway)
	if _, err := io.WriteString(existing, "one\n"); err != nil {
		t.Fatal(err)
	}
	waitForRelayConnections(t, gateway, 1)

	gateway.unpublish(1)
	if got := relayConnectionCount(gateway); got != 1 {
		t.Fatalf(
			"stale unpublish changed connection count to %d",
			got,
		)
	}
	gateway.unpublish(2)

	rejected := dialRelay(t, gateway)
	if err := rejected.SetReadDeadline(
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := rejected.Read(make([]byte, 1)); err == nil {
		t.Fatal("unpublished relay accepted a new connection")
	}
	_ = rejected.Close()

	releaseOnce.Do(func() {
		close(release)
	})
	response, err := io.ReadAll(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "reply:one\n" {
		t.Fatalf("existing connection response = %q", response)
	}
	_ = existing.Close()
}

func TestRelayConnectorInvalidationClosesConnection(t *testing.T) {
	socket, serverDone := startRelayTestServer(t, func(connection net.Conn) {
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	})
	defer func() {
		<-serverDone
	}()

	gateway, err := newRelay(relayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()

	connector := newTestRelayConnector()
	if err := gateway.publish(
		3,
		relayTestDialer(socket),
		func() (Connector, error) {
			return connector, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	connection := dialRelay(t, gateway)
	waitForRelayConnections(t, gateway, 1)
	connector.Close()

	if err := connection.SetReadDeadline(
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(make([]byte, 1)); err == nil {
		t.Fatal("invalidated connector left relay connection open")
	}
	_ = connection.Close()
}

func TestRelayBoundsUnauthenticatedConnections(t *testing.T) {
	socket, serverDone := startRelayTestServer(
		t,
		func(connection net.Conn) {
			defer connection.Close()
			_, _ = io.Copy(io.Discard, connection)
		},
	)
	defer func() {
		<-serverDone
	}()

	listener, err := net.ListenTCP(
		"tcp4",
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := newRelayWithListener(
		listener,
		relayOptions{MaxConnections: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()

	var connectors atomic.Int64
	if err := gateway.publish(
		4,
		relayTestDialer(socket),
		func() (Connector, error) {
			connectors.Add(1)
			return newTestRelayConnector(), nil
		},
	); err != nil {
		t.Fatal(err)
	}

	first := dialRelay(t, gateway)
	waitForRelayConnections(t, gateway, 1)

	second := dialRelay(t, gateway)
	if err := second.SetReadDeadline(
		time.Now().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection beyond relay limit remained open")
	}
	_ = second.Close()
	if got := connectors.Load(); got != 1 {
		t.Fatalf(
			"generation connectors = %d, want 1",
			got,
		)
	}

	_ = first.Close()
}

func startRelayTestServer(
	t *testing.T,
	handle func(net.Conn),
) (string, <-chan struct{}) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "data.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()

		connection, err := listener.Accept()
		if err != nil {
			return
		}
		handle(connection)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		if err := os.Remove(path); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	})

	return path, done
}

func relayTestDialer(path string) relayDataDialer {
	return func(ctx context.Context) (*net.UnixConn, error) {
		var dialer net.Dialer
		connection, err := dialer.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, err
		}
		unixConnection, ok := connection.(*net.UnixConn)
		if !ok {
			_ = connection.Close()
			return nil, fmt.Errorf(
				"relay test connection has type %T",
				connection,
			)
		}

		return unixConnection, nil
	}
}

func dialRelay(t *testing.T, gateway *relay) *net.TCPConn {
	t.Helper()

	parsed, err := url.Parse(gateway.baseURL())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("tcp4", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		_ = connection.Close()
		t.Fatalf("relay connection has type %T", connection)
	}

	return tcp
}

func waitForRelayConnections(
	t *testing.T,
	gateway *relay,
	want int,
) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if relayConnectionCount(gateway) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf(
		"relay connection count = %d, want %d",
		relayConnectionCount(gateway),
		want,
	)
}

func relayConnectionCount(gateway *relay) int {
	if gateway == nil {
		return 0
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()

	return len(gateway.connections)
}
