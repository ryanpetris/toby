package control

// The control HTTP server: a chi router that exposes the intrinsic /control
// WebSocket channel and any caller-supplied routes (e.g. the host HTTP proxy, the
// binary delivery handler). Token-authenticated routes are gated by middleware.

import (
	"context"
	"net"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
)

// ConnHandler handles an upgraded control connection for its lifetime.
type ConnHandler func(context.Context, net.Conn)

// Route is a caller-supplied HTTP route mounted on the control server. When Auth
// is set the route is gated by the endpoint's bearer-token middleware.
type Route struct {
	Pattern string
	Handler http.Handler
	Auth    bool
}

type Server struct {
	Endpoint Endpoint
	listener net.Listener
	http     *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

// ListenEndpoint starts the control server: a chi router mounting the given
// routes, each gated by the endpoint's bearer-token middleware when it sets Auth.
// The /control WebSocket channel is just one such route (see WebSocketHandler).
// The listen address defaults to an ephemeral localhost port; the bound address
// is reported in Server.Endpoint.
func ListenEndpoint(ctx context.Context, endpoint Endpoint, routes ...Route) (*Server, error) {
	address := endpoint.ListenAddress
	if address == "" {
		address = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	actual := endpoint
	actual.ListenAddress = listener.Addr().String()
	actual.Host = actual.ListenAddress
	server := &Server{Endpoint: actual, listener: listener, ctx: serverCtx, cancel: cancel}

	auth := tokenMiddleware(endpoint.Token)
	router := chi.NewRouter()
	for _, route := range routes {
		if route.Pattern == "" || route.Handler == nil {
			continue
		}
		if route.Auth {
			router.With(auth).Handle(route.Pattern, route.Handler)
		} else {
			router.Handle(route.Pattern, route.Handler)
		}
	}

	server.http = &http.Server{Handler: router, BaseContext: func(net.Listener) context.Context { return serverCtx }}
	go func() { _ = server.http.Serve(listener) }()
	return server, nil
}

func tokenMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		s.cancel()
		if s.http != nil {
			err = s.http.Close()
		} else if s.listener != nil {
			err = s.listener.Close()
		}
	})
	return err
}
