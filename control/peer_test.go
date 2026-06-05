package control

import (
	"context"
	"encoding/json"
	"net"
	"testing"
)

// pipePeers wires two peers over an in-memory connection: a server peer running
// the given handler, and a client peer for issuing Calls.
func pipePeers(t *testing.T, handler Handler) *Peer {
	t.Helper()
	clientConn, serverConn := net.Pipe()

	serverPeer := NewPeer(context.Background(), serverConn, handler)
	serverPeer.Start(nil)
	t.Cleanup(func() { _ = serverPeer.Close() })

	clientPeer := NewPeer(context.Background(), clientConn, nil)
	clientPeer.Start(nil)
	t.Cleanup(func() { _ = clientPeer.Close() })

	return clientPeer
}

func TestPeerCallReturnsHandlerResult(t *testing.T) {
	client := pipePeers(t, func(_ context.Context, data []byte) ([]byte, error) {
		req, err := DecodeRequest(data)
		if err != nil {
			return nil, err
		}
		return ResponseOK(req.ID, map[string]string{"echoed": req.Method}), nil
	})

	resp, err := client.Call(context.Background(), "ping", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	data, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["echoed"] != "ping" {
		t.Fatalf("result = %#v, want echoed=ping", result)
	}
}

func TestPeerCallPropagatesHandlerError(t *testing.T) {
	client := pipePeers(t, func(_ context.Context, data []byte) ([]byte, error) {
		req, _ := DecodeRequest(data)
		return ResponseError(req.ID, CodeInvalidParams, "nope", nil), nil
	})

	_, err := client.Call(context.Background(), "boom", nil)
	if err == nil || err.Error() != "nope" {
		t.Fatalf("err = %v, want \"nope\"", err)
	}
}

func TestPeerCallCanceledByContext(t *testing.T) {
	// Handler never responds, so the Call must observe its context deadline.
	client := pipePeers(t, func(ctx context.Context, _ []byte) ([]byte, error) {
		<-ctx.Done()
		return nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Call(ctx, "hang", nil); err == nil {
		t.Fatal("expected canceled context to fail the call")
	}
}
