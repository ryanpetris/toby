package tunnel

// Tests for the generic JSON-RPC control stream carried over Tunnel.Control.

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/stdio"

	"google.golang.org/grpc"
)

func TestControlStreamCarriesPeerCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverSide, clientSide := net.Pipe()
	grpcServer := grpc.NewServer()
	tunnelServer := NewServer(fakeProxy{body: "unused"}, nil)
	RegisterTunnelServer(grpcServer, tunnelServer)
	go func() { _ = grpcServer.Serve(stdio.NewListener(serverSide)) }()
	defer grpcServer.Stop()

	cc, client, err := Dial(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	stream, err := client.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	managerPeer := control.NewPeer(ctx, NewStreamConn(stream, nil), func(_ context.Context, data []byte) ([]byte, error) {
		req, err := control.DecodeRequest(data)
		if err != nil {
			return nil, err
		}
		var params map[string]string
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		return control.ResponseOK(req.ID, params["value"]), nil
	})
	managerPeer.Start(nil)
	defer managerPeer.Close()

	resp, err := tunnelServer.Call(ctx, "test.echo", map[string]string{"value": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := resp.Result.(string)
	if got != "ok" {
		t.Fatalf("result = %q", got)
	}
}
