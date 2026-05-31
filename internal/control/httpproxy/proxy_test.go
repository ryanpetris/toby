package httpproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyForwardsRequestAndAppliesConfiguredHeaders(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "debug=1" {
			t.Errorf("url = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer host-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Passthrough"); got != "yes" {
			t.Errorf("X-Passthrough = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		seen <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	svc := NewService(ServiceParams{HTTP: upstream.Client()})
	id, err := svc.Register(Target{BaseURL: upstream.URL + "/v1", Headers: http.Header{"Authorization": []string{"Bearer host-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://proxy/proxy/"+id+"/chat/completions?debug=1", strings.NewReader(`{"model":"alpha"}`))
	req.Header.Set("Authorization", "Bearer client-secret")
	req.Header.Set("X-Passthrough", "yes")
	w := httptest.NewRecorder()

	svc.HandleHTTP(context.Background(), w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if body := w.Body.String(); body != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	if body := <-seen; body != `{"model":"alpha"}` {
		t.Fatalf("upstream body = %q", body)
	}
}

func TestProxyDispatchesInternalHandlerTarget(t *testing.T) {
	svc := NewService(ServiceParams{})
	id, err := svc.Register(Target{Headers: http.Header{"X-Internal": []string{"set"}}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.URL.RawQuery != "debug=1" {
			t.Errorf("url = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("X-Internal"); got != "set" {
			t.Errorf("X-Internal = %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	})})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://proxy/proxy/"+id+"/mcp?debug=1", nil)
	w := httptest.NewRecorder()

	svc.HandleHTTP(context.Background(), w, req)

	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("response = %d %q", w.Code, w.Body.String())
	}
}
