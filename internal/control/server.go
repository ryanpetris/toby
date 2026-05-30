package control

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
)

type ConnHandler func(context.Context, net.Conn)

type HTTPHandler func(context.Context, http.ResponseWriter, *http.Request)

type HTTPRoute struct {
	Pattern string
	Handler HTTPHandler
}

type Server struct {
	Endpoint Endpoint
	listener net.Listener
	http     *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

func ListenEndpoint(ctx context.Context, endpoint Endpoint, handler ConnHandler, routes ...HTTPRoute) (*Server, error) {
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
	mux := http.NewServeMux()
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		conn, err := acceptWebSocket(w, r, endpoint.Token)
		if err != nil {
			return
		}
		handler(serverCtx, conn)
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		serveBinary(w, r, endpoint.Token, endpoint.BinarySource)
	})
	for _, route := range routes {
		route := route
		if route.Pattern == "" || route.Handler == nil {
			continue
		}
		mux.HandleFunc(route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			route.Handler(serverCtx, w, r)
		})
	}
	server.http = &http.Server{Handler: mux}
	go func() { _ = server.http.Serve(listener) }()
	return server, nil
}

func serveBinary(w http.ResponseWriter, r *http.Request, token string, source BinarySource) {
	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if source == nil {
		http.Error(w, "binary source is not configured", http.StatusInternalServerError)
		return
	}
	data, err := source()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
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
