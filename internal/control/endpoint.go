package control

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	EnvControlHost  = "TOBY_CONTROL_HOST"
	EnvControlToken = "TOBY_CONTROL_TOKEN"
)

type BinarySource func() ([]byte, error)

type Endpoint struct {
	ListenAddress string
	Host          string
	Token         string
	BinarySource  BinarySource
}

func WebSocketEndpoint(listenAddress, token string) Endpoint {
	return Endpoint{ListenAddress: listenAddress, Token: token}
}

func (e Endpoint) ControlURL() string {
	return "ws://" + e.Host + "/control"
}

func (e Endpoint) BinaryURL() string {
	return "http://" + e.Host + "/binary"
}

func (e Endpoint) ProxyBaseURL(id string) string {
	return "http://" + e.Host + "/proxy/" + url.PathEscape(id)
}

func DefaultEndpoint() (Endpoint, error) {
	host := strings.TrimSpace(os.Getenv(EnvControlHost))
	if host == "" {
		return Endpoint{}, fmt.Errorf("%s is required", EnvControlHost)
	}
	if err := validateHostPort(host); err != nil {
		return Endpoint{}, fmt.Errorf("invalid %s: %w", EnvControlHost, err)
	}
	return Endpoint{Host: host, Token: os.Getenv(EnvControlToken)}, nil
}

func validateHostPort(host string) error {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(port) == "" {
		return fmt.Errorf("must be host:port")
	}
	return nil
}
