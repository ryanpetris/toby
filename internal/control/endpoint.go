package control

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	EnvControlURL   = "TOBY_CONTROL_URL"
	EnvControlToken = "TOBY_CONTROL_TOKEN"
	EnvBinaryURL    = "TOBY_BINARY_URL"
)

type BinarySource func() ([]byte, error)

type Endpoint struct {
	ListenAddress string
	URL           string
	BinaryURL     string
	Token         string
	BinarySource  BinarySource
}

func WebSocketEndpoint(listenAddress, token string) Endpoint {
	return Endpoint{ListenAddress: listenAddress, Token: token}
}

func DefaultEndpoint() (Endpoint, error) {
	endpointURL := strings.TrimSpace(os.Getenv(EnvControlURL))
	if endpointURL == "" {
		return Endpoint{}, fmt.Errorf("%s is required", EnvControlURL)
	}
	parsed, err := url.Parse(endpointURL)
	if err != nil {
		return Endpoint{}, fmt.Errorf("invalid %s: %w", EnvControlURL, err)
	}
	if parsed.Scheme != "ws" || parsed.Host == "" {
		return Endpoint{}, fmt.Errorf("%s must be a ws:// URL", EnvControlURL)
	}
	return Endpoint{URL: endpointURL, Token: os.Getenv(EnvControlToken)}, nil
}
