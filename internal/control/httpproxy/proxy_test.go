package httpproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestParseProxyPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantID     string
		wantSuffix string
		wantErr    bool
	}{
		{name: "id only", path: "/proxy/id", wantID: "id"},
		{name: "escaped id and suffix", path: "/proxy/local%2Fdocs/v1%2Fmodels", wantID: "local/docs", wantSuffix: "/v1/models"},
		{name: "wrong prefix", path: "/other/id", wantErr: true},
		{name: "blank id", path: "/proxy/%20", wantErr: true},
		{name: "bad id escape", path: "/proxy/%ZZ", wantErr: true},
		{name: "bad suffix escape", path: "/proxy/id/%ZZ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, suffix, err := parseProxyPath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if id != tt.wantID || suffix != tt.wantSuffix {
				t.Fatalf("parseProxyPath = %q, %q; want %q, %q", id, suffix, tt.wantID, tt.wantSuffix)
			}
		})
	}
}

func TestTargetURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		suffix  string
		query   string
		want    string
		wantErr bool
	}{
		{name: "joins suffix", baseURL: " https://example.com/api/ ", suffix: "/v1/models", query: "debug=1", want: "https://example.com/api/v1/models?debug=1"},
		{name: "replaces query", baseURL: "https://example.com/path?old=1", query: "new=1", want: "https://example.com/path?new=1"},
		{name: "keeps base path", baseURL: "https://example.com/path", want: "https://example.com/path"},
		{name: "relative rejected", baseURL: "/path", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := targetURL(tt.baseURL, tt.suffix, tt.query)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("targetURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHeaderHelpersReplaceAndCloneValues(t *testing.T) {
	dst := http.Header{"X-Test": []string{"old"}, "X-Keep": []string{"keep"}}
	src := http.Header{"X-Test": []string{"new", "second"}}
	copyHeaders(dst, src)
	if got, want := dst.Values("X-Test"), []string{"new", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("X-Test = %#v, want %#v", got, want)
	}
	if dst.Get("X-Keep") != "keep" {
		t.Fatalf("X-Keep = %q", dst.Get("X-Keep"))
	}

	applyHeaders(dst, http.Header{"X-Keep": []string{"override"}})
	if dst.Get("X-Keep") != "override" {
		t.Fatalf("X-Keep after apply = %q", dst.Get("X-Keep"))
	}

	clone := cloneHeader(dst)
	dst.Add("X-Test", "mutated")
	if got, want := clone.Values("X-Test"), []string{"new", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cloned X-Test = %#v, want %#v", got, want)
	}
}
