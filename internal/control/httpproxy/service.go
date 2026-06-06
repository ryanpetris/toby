// Package httpproxy is a host-side reverse proxy that lets the sandbox reach host
// services without ever seeing their credentials. A caller registers a target —
// either an upstream URL (RegisterUpstream) or a local handler (RegisterHandler) —
// and receives an opaque id; HandleHTTP serves every registered target under
// /proxy/<id>/….
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
)

// Service holds the registered proxy targets and serves them.
type Service struct {
	mu      sync.RWMutex
	targets map[string]target
	http    *http.Client
}

// target is a single registered destination: either an upstream URL (with headers
// to apply) or a local handler. Exactly one form is set.
type target struct {
	baseURL string
	headers http.Header
	handler http.Handler
}

// RegisterUpstream registers a reverse-proxy target to baseURL, applying headers
// to every forwarded request, and returns its opaque id.
func (s *Service) RegisterUpstream(baseURL string, headers http.Header) (string, error) {
	if s == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}

	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("proxy baseURL is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("proxy baseURL must be an absolute URL")
	}

	return s.add(target{baseURL: strings.TrimSpace(baseURL), headers: cloneHeader(headers)}), nil
}

// RegisterHandler registers a local handler served under the returned id.
func (s *Service) RegisterHandler(h http.Handler) (string, error) {
	if s == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}

	return s.add(target{handler: h}), nil
}

// HandleHTTP serves a /proxy/<id>/… request, dispatching to the registered target.
func (s *Service) HandleHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	id, suffix, err := parseProxyPath(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	t, ok := s.lookup(id)
	if !ok {
		http.Error(w, "proxy target is not registered", http.StatusNotFound)
		return
	}
	if t.handler != nil {
		s.handleLocal(ctx, t, suffix, w, r)
		return
	}

	upstream, err := targetURL(t.baseURL, suffix, r.URL.RawQuery)
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
	applyHeaders(req.Header, t.headers)
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

func (s *Service) add(t target) string {
	id := uuid.NewString()
	s.mu.Lock()
	s.targets[id] = t
	s.mu.Unlock()
	return id
}

func (s *Service) handleLocal(ctx context.Context, t target, suffix string, w http.ResponseWriter, r *http.Request) {
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
	t.handler.ServeHTTP(w, req)
}

func (s *Service) lookup(id string) (target, bool) {
	if s == nil {
		return target{}, false
	}
	s.mu.RLock()
	t, ok := s.targets[id]
	s.mu.RUnlock()
	if !ok {
		return target{}, false
	}
	t.headers = cloneHeader(t.headers)
	return t, true
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
