package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Dial builds a gRPC client over a single pre-established net.Conn (the stdio
// link). The conn is handed to gRPC exactly once; any reconnect attempt fails,
// which is correct — if the stdio pipe breaks the session is over. No keepalive
// is configured, so gRPC never relies on deadlines the pipe can't honor.
func Dial(conn net.Conn) (*grpc.ClientConn, TunnelClient, error) {
	var once sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var c net.Conn
		once.Do(func() { c = conn })
		if c == nil {
			return nil, fmt.Errorf("stdio link already consumed")
		}
		return c, nil
	}
	cc, err := grpc.NewClient(
		"passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		return nil, nil, err
	}
	return cc, NewTunnelClient(cc), nil
}

// Forward tunnels one accepted local connection to the host over a Connect stream,
// copying bytes both ways until either side closes. It owns local and closes it on
// return.
func Forward(ctx context.Context, client TunnelClient, local net.Conn) error {
	defer local.Close()
	stream, err := client.Connect(ctx)
	if err != nil {
		return err
	}
	remote := newStreamConn(stream, nil)
	defer remote.Close()

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(remote, local) // sandbox client -> host
		_ = remote.CloseWrite()
		errc <- err
	}()
	go func() {
		_, err := io.Copy(local, remote) // host -> sandbox client
		if cw, ok := local.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		errc <- err
	}()
	err1 := <-errc
	err2 := <-errc
	if err1 != nil {
		return err1
	}
	return err2
}
