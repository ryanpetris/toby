package control

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
)

type Handler func(context.Context, []byte) ([]byte, error)

type Server struct {
	Path     string
	listener net.Listener
	handler  Handler
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

func Listen(ctx context.Context, path string, handler Handler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	server := &Server{Path: path, listener: listener, handler: handler, ctx: serverCtx, cancel: cancel}
	go server.serve()
	return server, nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		s.cancel()
		err = s.listener.Close()
		if removeErr := os.Remove(s.Path); err == nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = removeErr
		}
	})
	return err
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	request, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	response, err := s.handler(s.ctx, request)
	if len(response) == 0 && err != nil {
		response = ResponseError(nil, CodeInternalError, err.Error(), nil)
	}
	_, _ = conn.Write(response)
}
