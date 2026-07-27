package remotehttp

// Exercises per-connector bridge calls, header cloning, and run revocation.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

func TestResolverCreatesIndependentBridgeCallPerConnector(t *testing.T) {
	t.Parallel()

	bridge := &recordingBridge{
		started: make(chan httpbridge.Upstream, 2),
	}
	resolver, err := NewResolver(bridge, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "remote",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetRemote,
				Transport: mcpgateway.TransportHTTP,
				URL:       "https://secret.example.invalid/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer secret",
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	contexts := make([]context.CancelFunc, 0, 2)
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		contexts = append(contexts, cancel)
		go target.Target().ServeConnector(ctx, nil)
	}

	for range 2 {
		select {
		case upstream := <-bridge.started:
			if upstream.Endpoint != "https://secret.example.invalid/mcp" {
				t.Fatalf("bridge endpoint = %q", upstream.Endpoint)
			}
			if got := upstream.Headers.Get("Authorization"); got != "Bearer secret" {
				t.Fatalf("bridge Authorization = %q", got)
			}
			upstream.Headers.Set("Authorization", "changed")
		case <-time.After(time.Second):
			t.Fatal("connector did not start a bridge session")
		}
	}

	target.Revoke()
	for _, cancel := range contexts {
		cancel()
	}
	eventually(t, time.Second, func() bool {
		return bridge.activeCount() == 0
	})
}

type recordingBridge struct {
	mu      sync.Mutex
	active  int
	started chan httpbridge.Upstream
}

var _ Bridge = (*recordingBridge)(nil)

func (b *recordingBridge) Serve(
	ctx context.Context,
	_ io.ReadWriteCloser,
	upstream httpbridge.Upstream,
) error {
	b.mu.Lock()
	b.active++
	b.mu.Unlock()

	b.started <- httpbridge.Upstream{
		Endpoint: upstream.Endpoint,
		Headers:  upstream.Headers.Clone(),
	}
	<-ctx.Done()

	b.mu.Lock()
	b.active--
	b.mu.Unlock()
	return ctx.Err()
}

func (b *recordingBridge) activeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

func eventually(
	t *testing.T,
	timeout time.Duration,
	condition func() bool,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
