package httpbridge

// Restricts configured headers to the selected upstream origin.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	protocolVersionHeader = "Mcp-Protocol-Version"
	sessionIDHeader       = "Mcp-Session-Id"
)

var forbiddenConfiguredHeaders = map[string]struct{}{
	"Accept":               {},
	"Connection":           {},
	"Content-Length":       {},
	"Content-Type":         {},
	"Host":                 {},
	"Keep-Alive":           {},
	"Last-Event-Id":        {},
	"Mcp-Protocol-Version": {},
	"Mcp-Session-Id":       {},
	"Proxy-Authorization":  {},
	"Proxy-Connection":     {},
	"Te":                   {},
	"Trailer":              {},
	"Transfer-Encoding":    {},
	"Upgrade":              {},
}

// ValidateConfiguredHeaders rejects headers whose names or values are unsafe
// for HTTP forwarding and headers whose values the bridge must control.
func ValidateConfiguredHeaders(headers http.Header) error {
	_, err := cloneHeaders(headers)

	return err
}

type headerTransport struct {
	base            http.RoundTripper
	origin          origin
	headers         http.Header
	state           *sessionState
	responses       *responseLimiter
	closeTimeout    time.Duration
	maxMessageBytes int
	logger          *diagnostic.Logger
}

var _ http.RoundTripper = (*headerTransport)(nil)

type origin struct {
	scheme   string
	hostname string
	port     string
}

func parseEndpoint(endpoint string) (origin, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return origin{}, fmt.Errorf("parse MCP HTTP endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return origin{}, errors.New("MCP HTTP endpoint scheme must be http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return origin{}, errors.New("MCP HTTP endpoint must include a host")
	}
	if parsed.User != nil {
		return origin{}, errors.New("MCP HTTP endpoint must not include user information")
	}
	if parsed.Fragment != "" {
		return origin{}, errors.New("MCP HTTP endpoint must not include a fragment")
	}

	return origin{
		scheme:   strings.ToLower(parsed.Scheme),
		hostname: strings.ToLower(parsed.Hostname()),
		port:     normalizedOriginPort(parsed.Scheme, parsed.Port()),
	}, nil
}

func cloneHeaders(headers http.Header) (http.Header, error) {
	cloned := make(http.Header, len(headers))
	for name, values := range headers {
		if name != strings.TrimSpace(name) || !validHeaderName(name) {
			return nil, errors.New("configured MCP HTTP header name is invalid")
		}
		canonical := http.CanonicalHeaderKey(name)
		if _, forbidden := forbiddenConfiguredHeaders[canonical]; forbidden {
			return nil, fmt.Errorf("configured MCP HTTP header %q is reserved", canonical)
		}
		if _, duplicate := cloned[canonical]; duplicate {
			return nil, fmt.Errorf("configured MCP HTTP header %q appears more than once", canonical)
		}
		for _, value := range values {
			if !validHeaderValue(value) {
				return nil, fmt.Errorf("configured MCP HTTP header %q has an invalid value", canonical)
			}
		}

		cloned[canonical] = append([]string(nil), values...)
	}

	return cloned, nil
}

func sessionHTTPClient(
	shared *http.Client,
	endpointOrigin origin,
	state *sessionState,
	responses *responseLimiter,
	headers http.Header,
	closeTimeout time.Duration,
	maxMessageBytes int,
	logger *diagnostic.Logger,
) (*http.Client, error) {
	source := *shared

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create isolated MCP HTTP cookie jar: %w", err)
	}
	source.Jar = jar

	base := source.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	source.Transport = &headerTransport{
		base:            base,
		origin:          endpointOrigin,
		headers:         headers,
		state:           state,
		responses:       responses,
		closeTimeout:    closeTimeout,
		maxMessageBytes: maxMessageBytes,
		logger:          logger,
	}

	return &source, nil
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !t.origin.matches(request.URL) {
		return nil, errors.New("refusing to send an MCP HTTP request outside its configured origin")
	}
	if request.Method == http.MethodDelete && !t.state.hasSession() {
		return &http.Response{
			Status:     "204 No Content",
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	}

	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for name, values := range t.headers {
		cloned.Header.Del(name)
		for _, value := range values {
			cloned.Header.Add(name, value)
		}
	}
	if version := t.state.protocolVersion(); version != "" {
		cloned.Header.Set(protocolVersionHeader, version)
	}

	var cancel context.CancelFunc
	if request.Method == http.MethodDelete && t.closeTimeout > 0 {
		clonedContext, timeoutCancel := context.WithTimeout(cloned.Context(), t.closeTimeout)
		cloned = cloned.WithContext(clonedContext)
		cancel = timeoutCancel
	}
	if cancel != nil {
		defer cancel()
	}

	cloned = cloned.WithContext(httptrace.WithClientTrace(
		cloned.Context(),
		&httptrace.ClientTrace{
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					signalRequestDispatched(cloned.Context())
				}
			},
		},
	))
	response, err := t.base.RoundTrip(cloned)
	// Custom RoundTrippers need not emit httptrace callbacks. Unblock the
	// relay once one returns so an alternate transport cannot deadlock it.
	if err == nil {
		signalRequestDispatched(cloned.Context())
	}
	if response != nil && response.Body != nil {
		body, limitErr := t.responses.wrap(response.Body)
		if limitErr != nil {
			t.logger.DebugError(
				"close rejected MCP HTTP response body",
				response.Body.Close(),
			)
			return nil, errors.Join(err, limitErr)
		}

		mediaType, _, mediaErr := mime.ParseMediaType(
			response.Header.Get("Content-Type"),
		)
		if mediaErr != nil {
			mediaType = ""
		}
		response.Body = newResponseLimitReadCloser(
			body,
			t.maxMessageBytes,
			strings.EqualFold(mediaType, "text/event-stream"),
			func() {
				t.state.markMessageLimitExceeded(t.maxMessageBytes)
			},
		)
	}
	if request.Method == http.MethodDelete && response != nil && response.Body != nil {
		_, drainErr := io.Copy(io.Discard, response.Body)
		t.logger.DebugError(
			"drain MCP HTTP session-close response",
			drainErr,
		)
		t.logger.DebugError(
			"close MCP HTTP session-close response body",
			response.Body.Close(),
		)
	}

	return response, err
}

func (o origin) matches(target *url.URL) bool {
	if target == nil || target.User != nil {
		return false
	}

	return strings.EqualFold(target.Scheme, o.scheme) &&
		strings.EqualFold(target.Hostname(), o.hostname) &&
		normalizedOriginPort(target.Scheme, target.Port()) == o.port
}

func normalizedOriginPort(scheme string, port string) string {
	switch {
	case strings.EqualFold(scheme, "http") && port == "80":
		return ""
	case strings.EqualFold(scheme, "https") && port == "443":
		return ""
	default:
		return port
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}

	for _, character := range []byte(name) {
		if !isTokenCharacter(character) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\t' {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func isTokenCharacter(character byte) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return true
	}

	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))
}
