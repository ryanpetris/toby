package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	"petris.dev/toby/internal/control"

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

	controlMu      sync.Mutex
	control        *control.Peer
	controlCh      chan *control.Peer
	controlHandler control.Handler
}

// NewServer wires a tunnel server to proxy. onReady (optional) fires when the
// sandbox manager reports its local listener is bound.
func NewServer(proxy ProxyHandler, onReady func(addr string)) *Server {
	s := &Server{
		proxy:     proxy,
		onReady:   onReady,
		lis:       &chanListener{conns: make(chan net.Conn), done: make(chan struct{})},
		controlCh: make(chan *control.Peer, 1),
	}
	// The home tunnel carries only the Control stream (files + exec) — no reverse
	// proxy — so the http server is started only when a proxy is wired (the netns side).
	if proxy != nil {
		s.httpSrv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.proxy.HandleHTTP(r.Context(), w, r)
		})}
		go func() { _ = s.httpSrv.Serve(s.lis) }()
	}
	return s
}

// SetControlHandler installs the handler for inbound Control-stream messages from the
// manager (e.g. streamed exec.output notifications). Set before the manager connects.
func (s *Server) SetControlHandler(h control.Handler) {
	s.controlMu.Lock()
	s.controlHandler = h
	s.controlMu.Unlock()
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

// Control is the manager-initiated JSON-RPC control stream.
func (s *Server) Control(stream grpc.BidiStreamingServer[Chunk, Chunk]) error {
	conn := newStreamConn(stream, nil)
	s.controlMu.Lock()
	handler := s.controlHandler
	s.controlMu.Unlock()
	peer := control.NewPeer(stream.Context(), conn, handler)
	peer.Start(nil)

	s.controlMu.Lock()
	old := s.control
	s.control = peer
	select {
	case s.controlCh <- peer:
	default:
	}
	s.controlMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	defer func() {
		s.controlMu.Lock()
		if s.control == peer {
			s.control = nil
		}
		s.controlMu.Unlock()
		_ = peer.Close()
	}()
	<-peer.Done()
	return peer.Err()
}

// Call sends one JSON-RPC request to the sandbox manager over the control stream.
func (s *Server) Call(ctx context.Context, method string, params any) (control.RPCResponse, error) {
	peer, err := s.controlPeer(ctx)
	if err != nil {
		return control.RPCResponse{}, err
	}
	return peer.Call(ctx, method, params)
}

func (s *Server) controlPeer(ctx context.Context) (*control.Peer, error) {
	s.controlMu.Lock()
	peer := s.control
	s.controlMu.Unlock()
	if peer != nil {
		return peer, nil
	}

	select {
	case peer := <-s.controlCh:
		return peer, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.lis.done:
		return nil, fmt.Errorf("sandbox manager control stream is not connected")
	}
}

// Close stops the embedded http.Server and its listener.
func (s *Server) Close() error {
	s.controlMu.Lock()
	if s.control != nil {
		_ = s.control.Close()
	}
	s.controlMu.Unlock()
	s.lis.Close()
	if s.httpSrv != nil {
		return s.httpSrv.Close()
	}
	return nil
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
