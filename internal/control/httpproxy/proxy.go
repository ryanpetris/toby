package httpproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/fx"
)

type ServiceParams struct {
	fx.In

	HTTP *http.Client `optional:"true"`
}

type Target struct {
	BaseURL string
	Headers http.Header
	Handler http.Handler
}

type Service struct {
	mu      sync.RWMutex
	targets map[string]Target
	http    *http.Client
}

func NewService(params ServiceParams) *Service {
	client := &http.Client{}
	if params.HTTP != nil && params.HTTP.Transport != nil {
		client.Transport = params.HTTP.Transport
	}
	return &Service{targets: map[string]Target{}, http: client}
}

func (s *Service) Register(target Target) (string, error) {
	if s == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}
	if target.Handler != nil {
		return s.register(target), nil
	}
	baseURL, err := url.Parse(strings.TrimSpace(target.BaseURL))
	if err != nil {
		return "", fmt.Errorf("proxy baseURL is invalid: %w", err)
	}
	if baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("proxy baseURL must be an absolute URL")
	}
	return s.register(target), nil
}

func (s *Service) HandleHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, suffix, err := parseProxyPath(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	target, ok := s.target(id)
	if !ok {
		http.Error(w, "proxy target is not registered", http.StatusNotFound)
		return
	}
	if target.Handler != nil {
		s.handleTarget(ctx, target, suffix, w, r)
		return
	}
	upstream, err := targetURL(target.BaseURL, suffix, r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, upstream, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	copyHeaders(req.Header, r.Header)
	applyHeaders(req.Header, target.Headers)
	resp, err := s.http.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("proxy request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	copyBody(w, resp.Body)
}

func (s *Service) register(target Target) string {
	id := uuid.NewString()
	s.mu.Lock()
	s.targets[id] = Target{BaseURL: strings.TrimSpace(target.BaseURL), Headers: cloneHeader(target.Headers), Handler: target.Handler}
	s.mu.Unlock()
	return id
}

func (s *Service) handleTarget(ctx context.Context, target Target, suffix string, w http.ResponseWriter, r *http.Request) {
	req := r.Clone(ctx)
	clonedURL := *r.URL
	clonedURL.RawPath = ""
	if suffix == "" {
		clonedURL.Path = "/"
	} else {
		clonedURL.Path = suffix
	}
	req.URL = &clonedURL
	req.RequestURI = ""
	req.Header = cloneHeader(r.Header)
	applyHeaders(req.Header, target.Headers)
	target.Handler.ServeHTTP(w, req)
}

func (s *Service) target(id string) (Target, bool) {
	if s == nil {
		return Target{}, false
	}
	s.mu.RLock()
	target, ok := s.targets[id]
	s.mu.RUnlock()
	if !ok {
		return Target{}, false
	}
	target.Headers = cloneHeader(target.Headers)
	return target, true
}

func parseProxyPath(escapedPath string) (string, string, error) {
	const prefix = "/proxy/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", "", fmt.Errorf("proxy path must start with %s", prefix)
	}
	remaining := strings.TrimPrefix(escapedPath, prefix)
	segment, suffix, _ := strings.Cut(remaining, "/")
	id, err := url.PathUnescape(segment)
	if err != nil || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("proxy id is required")
	}
	if suffix == "" {
		return id, "", nil
	}
	decodedSuffix, err := url.PathUnescape(suffix)
	if err != nil {
		return "", "", fmt.Errorf("invalid proxy path")
	}
	return id, "/" + decodedSuffix, nil
}

func targetURL(baseURL, suffix, rawQuery string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("proxy baseURL must be an absolute URL")
	}
	if suffix != "" {
		basePath := strings.TrimRight(parsed.Path, "/")
		suffix := strings.TrimLeft(suffix, "/")
		parsed.RawPath = ""
		parsed.Path = basePath + "/" + suffix
	}
	parsed.RawQuery = rawQuery
	return parsed.String(), nil
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func applyHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeader(src http.Header) http.Header {
	clone := http.Header{}
	for key, values := range src {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func copyBody(w http.ResponseWriter, body io.Reader) {
	flusher, flush := w.(http.Flusher)
	if !flush {
		_, _ = io.Copy(w, body)
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}
