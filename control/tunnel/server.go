package tunnel

import (
	"context"
	"net"
	"net/http"
	"sync"

	"google.golang.org/grpc"
)

// ProxyHandler is the host reverse proxy that injects credentials and dials the
// real upstream. *httpproxy.Service satisfies it.
type ProxyHandler interface {
	HandleHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request)
}

// Server implements the gRPC TunnelServer on the host. Each Connect stream is one
// proxied connection from the sandbox; it is adapted to a net.Conn and fed to an
// in-process http.Server whose handler is the host reverse proxy. The sandbox thus
// reaches MCP servers and providers without ever holding their credentials.
type Server struct {
	UnimplementedTunnelServer

	proxy   ProxyHandler
	onReady func(addr string)

	lis     *chanListener
	httpSrv *http.Server
}

// NewServer wires a tunnel server to proxy. onReady (optional) fires when the
// sandbox manager reports its local listener is bound.
func NewServer(proxy ProxyHandler, onReady func(addr string)) *Server {
	s := &Server{
		proxy:   proxy,
		onReady: onReady,
		lis:     &chanListener{conns: make(chan net.Conn), done: make(chan struct{})},
	}
	s.httpSrv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.proxy.HandleHTTP(r.Context(), w, r)
	})}
	go func() { _ = s.httpSrv.Serve(s.lis) }()
	return s
}

// Ready signals the sandbox is up; req.Addr is the manager's bound proxy address.
func (s *Server) Ready(_ context.Context, req *ReadyRequest) (*ReadyResponse, error) {
	if s.onReady != nil {
		s.onReady(req.GetAddr())
	}
	return &ReadyResponse{}, nil
}

// Connect feeds one proxied connection into the host reverse proxy. It hands the
// stream-backed conn to the http.Server and blocks until that server is done with
// it (or the stream ends), so the gRPC stream outlives the HTTP exchange.
func (s *Server) Connect(stream grpc.BidiStreamingServer[Chunk, Chunk]) error {
	conn := newStreamConn(stream, nil)
	select {
	case s.lis.conns <- conn:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	select {
	case <-conn.Done():
		return nil
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

// Close stops the embedded http.Server and its listener.
func (s *Server) Close() error {
	s.lis.Close()
	return s.httpSrv.Close()
}

// chanListener feeds tunneled conns to the embedded http.Server.
type chanListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return streamAddr{} }
