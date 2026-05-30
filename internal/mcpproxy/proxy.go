package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/tobyconfig"

	"go.uber.org/fx"
)

const (
	streamableTransport = "streamable-http"
	sseTransport        = "sse"

	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "Mcp-Protocol-Version"
)

type ServiceParams struct {
	fx.In

	Config *tobyconfig.Service `optional:"true"`
}

type Service struct {
	config *tobyconfig.Service
}

type serverConfig struct {
	Name      string
	Transport string
	URL       string
	Headers   http.Header
}

func NewService(params ServiceParams) *Service {
	return &Service{config: params.Config}
}

func RunSandbox(ctx context.Context, name string, stdin io.Reader, stdout io.Writer) error {
	endpoint, err := control.DefaultEndpoint()
	if err != nil {
		return err
	}
	conn, err := control.DialEndpoint(endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()

	request, err := control.NewMCPProxyRequest(1, name)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	response, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	resp, err := control.DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	return bridgeRaw(ctx, stdin, conn, reader, stdout, conn.Close)
}

func bridgeRaw(ctx context.Context, leftReader io.Reader, leftWriter io.Writer, rightReader io.Reader, rightWriter io.Writer, close func() error) error {
	errs := make(chan error, 2)
	copyOne := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		if close != nil {
			_ = close()
		}
		errs <- err
	}
	go copyOne(leftWriter, leftReader)
	go copyOne(rightWriter, rightReader)

	select {
	case err := <-errs:
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		if close != nil {
			_ = close()
		}
		return ctx.Err()
	}
}

func (s *Service) Proxy(ctx context.Context, name string, conn net.Conn, reader *bufio.Reader) error {
	config, err := s.configFor(name)
	if err != nil {
		return err
	}
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-proxyCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	switch config.Transport {
	case sseTransport:
		return newSSEProxy(config, conn, conn.Close).run(proxyCtx, reader)
	default:
		return newStreamableProxy(config, conn, conn.Close).run(proxyCtx, reader)
	}
}

func (s *Service) Validate(name string) error {
	_, err := s.configFor(name)
	return err
}

func (s *Service) configFor(name string) (serverConfig, error) {
	if s == nil || s.config == nil {
		return serverConfig{}, fmt.Errorf("mcp proxy configuration is not available")
	}
	servers := s.config.MCPServers()
	configured, ok := servers[name]
	if !ok {
		return serverConfig{}, fmt.Errorf("mcp server %q is not configured", name)
	}
	if !configured.Enabled() {
		return serverConfig{}, fmt.Errorf("mcp server %q is disabled", name)
	}
	raw := configured.Raw()
	if !tobyconfig.MCPServerHTTPProxyable(raw) {
		return serverConfig{}, fmt.Errorf("mcp server %q is not an HTTP MCP server", name)
	}
	endpoint, _ := raw["url"].(string)
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return serverConfig{}, fmt.Errorf("mcp server %q url is required", name)
	}
	transport := streamableTransport
	if typ, _ := raw["type"].(string); strings.TrimSpace(typ) == sseTransport {
		transport = sseTransport
	}
	headers, err := configuredHeaders(raw)
	if err != nil {
		return serverConfig{}, fmt.Errorf("mcp server %q: %w", name, err)
	}
	return serverConfig{Name: name, Transport: transport, URL: endpoint, Headers: headers}, nil
}

func configuredHeaders(raw map[string]any) (http.Header, error) {
	headers := http.Header{}
	for _, key := range []string{"headers", "http_headers"} {
		if err := mergeHeaderMap(headers, raw[key]); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	if err := mergeEnvHeaderMap(headers, raw["env_http_headers"]); err != nil {
		return nil, fmt.Errorf("env_http_headers: %w", err)
	}
	if name, _ := raw["bearer_token_env_var"].(string); strings.TrimSpace(name) != "" && headers.Get("Authorization") == "" {
		if token := os.Getenv(strings.TrimSpace(name)); token != "" {
			headers.Set("Authorization", "Bearer "+token)
		}
	}
	return headers, nil
}

func mergeHeaderMap(headers http.Header, raw any) error {
	if raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("must be an object")
	}
	for name, rawValue := range values {
		switch value := rawValue.(type) {
		case string:
			headers.Set(name, value)
		case []any:
			headers.Del(name)
			for _, item := range value {
				text, ok := item.(string)
				if !ok {
					return fmt.Errorf("header %q entries must be strings", name)
				}
				headers.Add(name, text)
			}
		default:
			return fmt.Errorf("header %q value must be a string or string array", name)
		}
	}
	return nil
}

func mergeEnvHeaderMap(headers http.Header, raw any) error {
	if raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("must be an object")
	}
	for headerName, rawEnvName := range values {
		envName, ok := rawEnvName.(string)
		if !ok || strings.TrimSpace(envName) == "" {
			return fmt.Errorf("header %q env var name must be a string", headerName)
		}
		if value := os.Getenv(strings.TrimSpace(envName)); value != "" {
			headers.Set(headerName, value)
		}
	}
	return nil
}

type streamableProxy struct {
	cfg    serverConfig
	client *http.Client
	out    io.Writer
	close  func() error

	writeMu sync.Mutex
	mu      sync.Mutex
	session string
	version string

	standaloneOnce sync.Once
	failOnce       sync.Once
	failErr        error
}

func newStreamableProxy(config serverConfig, out io.Writer, close func() error) *streamableProxy {
	return &streamableProxy{cfg: config, client: &http.Client{}, out: out, close: close}
}

func (p *streamableProxy) run(ctx context.Context, reader *bufio.Reader) error {
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if postErr := p.post(ctx, bytes.TrimSpace(line)); postErr != nil {
				return postErr
			}
		}
		if err != nil {
			if err := p.failure(); err != nil {
				return err
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func (p *streamableProxy) post(ctx context.Context, data []byte) error {
	initID := initializeRequestID(data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	applyHeaders(req.Header, p.cfg.Headers)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	p.applyMCPHeaders(req.Header)

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	if session := resp.Header.Get(headerSessionID); session != "" {
		if err := p.setSession(session); err != nil {
			resp.Body.Close()
			return err
		}
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		resp.Body.Close()
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(resp)
	}
	contentType := responseContentType(resp)
	switch contentType {
	case "application/json":
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := p.writeMessage(body); err != nil {
				return err
			}
			p.maybeInitialized(ctx, body, initID)
		}
		return nil
	case "text/event-stream":
		go p.handleEventStream(ctx, resp, initID, false)
		return nil
	default:
		resp.Body.Close()
		return fmt.Errorf("mcp server %q returned unsupported content type %q", p.cfg.Name, contentType)
	}
}

func (p *streamableProxy) applyMCPHeaders(headers http.Header) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.version != "" {
		headers.Set(headerProtocolVersion, p.version)
	}
	if p.session != "" {
		headers.Set(headerSessionID, p.session)
	}
}

func (p *streamableProxy) setSession(session string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == "" {
		p.session = session
		return nil
	}
	if p.session != session {
		return fmt.Errorf("mcp server %q returned mismatched session IDs %q and %q", p.cfg.Name, p.session, session)
	}
	return nil
}

func (p *streamableProxy) maybeInitialized(ctx context.Context, data []byte, initID string) {
	if initID == "" {
		return
	}
	version := initializeProtocolVersion(data, initID)
	if version == "" {
		return
	}
	p.mu.Lock()
	p.version = version
	p.mu.Unlock()
	p.standaloneOnce.Do(func() {
		go p.openStandaloneSSE(ctx)
	})
}

func (p *streamableProxy) openStandaloneSSE(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.URL, nil)
	if err != nil {
		p.fail(err)
		return
	}
	applyHeaders(req.Header, p.cfg.Headers)
	req.Header.Set("Accept", "text/event-stream")
	p.applyMCPHeaders(req.Header)
	resp, err := p.client.Do(req)
	if err != nil {
		p.fail(err)
		return
	}
	if resp.StatusCode == http.StatusMethodNotAllowed || (resp.StatusCode >= 400 && resp.StatusCode < 500) {
		resp.Body.Close()
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.fail(httpError(resp))
		return
	}
	if responseContentType(resp) != "text/event-stream" {
		resp.Body.Close()
		p.fail(fmt.Errorf("mcp server %q standalone stream returned unsupported content type", p.cfg.Name))
		return
	}
	p.handleEventStream(req.Context(), resp, "", true)
}

func (p *streamableProxy) handleEventStream(ctx context.Context, resp *http.Response, initID string, standalone bool) {
	defer resp.Body.Close()
	scanner := newSSEEventScanner(resp.Body)
	for {
		event, ok, err := scanner.Next()
		if err != nil {
			if ctx.Err() == nil {
				p.fail(err)
			}
			return
		}
		if !ok {
			return
		}
		if event.Name != "" && event.Name != "message" {
			continue
		}
		if len(bytes.TrimSpace(event.Data)) == 0 {
			continue
		}
		if err := p.writeMessage(event.Data); err != nil {
			p.fail(err)
			return
		}
		p.maybeInitialized(ctx, event.Data, initID)
	}
}

func (p *streamableProxy) writeMessage(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.out.Write(append(bytes.TrimSpace(data), '\n'))
	return err
}

func (p *streamableProxy) fail(err error) {
	if err == nil {
		return
	}
	p.failOnce.Do(func() {
		p.mu.Lock()
		p.failErr = err
		p.mu.Unlock()
		if p.close != nil {
			_ = p.close()
		}
	})
}

func (p *streamableProxy) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failErr
}

type sseProxy struct {
	cfg    serverConfig
	client *http.Client
	out    io.Writer
	close  func() error

	writeMu  sync.Mutex
	failMu   sync.Mutex
	failOnce sync.Once
	failErr  error
	postURL  string
}

func newSSEProxy(config serverConfig, out io.Writer, close func() error) *sseProxy {
	return &sseProxy{cfg: config, client: &http.Client{}, out: out, close: close}
}

func (p *sseProxy) run(ctx context.Context, reader *bufio.Reader) error {
	resp, scanner, endpoint, err := p.connect(ctx)
	if err != nil {
		return err
	}
	p.postURL = endpoint
	go p.readEvents(ctx, resp, scanner)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if postErr := p.post(ctx, bytes.TrimSpace(line)); postErr != nil {
				return postErr
			}
		}
		if err != nil {
			if err := p.failure(); err != nil {
				return err
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func (p *sseProxy) connect(ctx context.Context) (*http.Response, *sseEventScanner, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.URL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	applyHeaders(req.Header, p.cfg.Headers)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, "", httpError(resp)
	}
	if responseContentType(resp) != "text/event-stream" {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("mcp server %q returned unsupported content type", p.cfg.Name)
	}
	scanner := newSSEEventScanner(resp.Body)
	event, ok, err := scanner.Next()
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", err
	}
	if !ok || event.Name != "endpoint" {
		resp.Body.Close()
		return nil, nil, "", fmt.Errorf("mcp server %q did not send an SSE endpoint event", p.cfg.Name)
	}
	endpoint, err := resolveEndpoint(p.cfg.URL, string(event.Data))
	if err != nil {
		resp.Body.Close()
		return nil, nil, "", err
	}
	return resp, scanner, endpoint, nil
}

func (p *sseProxy) post(ctx context.Context, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.postURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	applyHeaders(req.Header, p.cfg.Headers)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(resp)
	}
	return nil
}

func (p *sseProxy) readEvents(ctx context.Context, resp *http.Response, scanner *sseEventScanner) {
	defer resp.Body.Close()
	for {
		event, ok, err := scanner.Next()
		if err != nil {
			if ctx.Err() == nil {
				p.fail(err)
			}
			return
		}
		if !ok {
			p.fail(io.EOF)
			return
		}
		if event.Name != "" && event.Name != "message" {
			continue
		}
		if len(bytes.TrimSpace(event.Data)) == 0 {
			continue
		}
		if err := p.writeMessage(event.Data); err != nil {
			p.fail(err)
			return
		}
	}
}

func (p *sseProxy) writeMessage(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.out.Write(append(bytes.TrimSpace(data), '\n'))
	return err
}

func (p *sseProxy) fail(err error) {
	if err == nil {
		return
	}
	p.failOnce.Do(func() {
		p.failMu.Lock()
		p.failErr = err
		p.failMu.Unlock()
		if p.close != nil {
			_ = p.close()
		}
	})
}

func (p *sseProxy) failure() error {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	return p.failErr
}

func applyHeaders(dst, src http.Header) {
	for name, values := range src {
		dst.Del(name)
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func responseContentType(resp *http.Response) string {
	return strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("mcp HTTP proxy request failed: %s: %s", resp.Status, message)
}

func resolveEndpoint(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	resolved, err := base.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	return resolved.String(), nil
}

func initializeRequestID(data []byte) string {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return ""
	}
	if request.Method != "initialize" || len(request.ID) == 0 {
		return ""
	}
	return string(bytes.TrimSpace(request.ID))
}

func initializeProtocolVersion(data []byte, initID string) string {
	var response struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return ""
	}
	if string(bytes.TrimSpace(response.ID)) != initID {
		return ""
	}
	return strings.TrimSpace(response.Result.ProtocolVersion)
}

type sseEvent struct {
	Name string
	Data []byte
}

type sseEventScanner struct {
	scanner *bufio.Scanner
}

func newSSEEventScanner(reader io.Reader) *sseEventScanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	return &sseEventScanner{scanner: scanner}
}

func (s *sseEventScanner) Next() (sseEvent, bool, error) {
	var name string
	var data []string
	for s.scanner.Scan() {
		line := strings.TrimSuffix(s.scanner.Text(), "\r")
		if line == "" {
			if len(data) == 0 {
				name = ""
				continue
			}
			return sseEvent{Name: eventName(name), Data: []byte(strings.Join(data, "\n"))}, true, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if ok && strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		}
	}
	if err := s.scanner.Err(); err != nil {
		return sseEvent{}, false, err
	}
	if len(data) > 0 {
		return sseEvent{Name: eventName(name), Data: []byte(strings.Join(data, "\n"))}, true, nil
	}
	return sseEvent{}, false, nil
}

func eventName(name string) string {
	if name == "" {
		return "message"
	}
	return name
}
