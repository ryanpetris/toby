// Package host owns the shared HTTP reverse proxy and dispatches method
// capabilities (the host-side Git tools) through a control.Router. The Git MCP
// tools call Handle in-process.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"

	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
)

// Service routes control method requests to their capabilities and holds the
// shared HTTP reverse proxy used by the tunnel and the Toby MCP server.
type Service struct {
	router *control.Router

	HTTPProxy *httpproxy.Service
}

func NewService(capabilities []control.Capability, httpProxy *httpproxy.Service) (*Service, error) {
	router, err := control.NewRouter(capabilities)
	if err != nil {
		return nil, err
	}
	return &Service{router: router, HTTPProxy: httpProxy}, nil
}

// Handle decodes an encoded control request and dispatches it to the matching
// capability. It is called in-process by the host Git client.
func (m *Service) Handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		var syntaxErr *json.SyntaxError
		code := control.CodeInvalidRequest
		if errors.As(err, &syntaxErr) {
			code = control.CodeParseError
		}
		return control.ResponseError(nil, code, err.Error(), nil), syscall.EINVAL
	}
	return m.router.Handle(ctx, req)
}
