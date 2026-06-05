package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"petris.dev/toby/providers"
)

func TestGetModelsRequiresHTTPClient(t *testing.T) {
	s := New(nil)
	if _, err := s.LookupModels(context.Background(), "https://example.test", nil); err == nil {
		t.Fatal("expected nil HTTP client to fail")
	}
}

func TestGetModelsUsesIDAsDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if userAgent := r.Header.Get("User-Agent"); userAgent == "" {
			t.Fatal("missing User-Agent header")
		}
		if header := r.Header.Get("X-Test"); header != "value" {
			t.Fatalf("X-Test = %q, want value", header)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"},{"id":""},{"id":"alpha"}]}`))
	}))
	t.Cleanup(server.Close)

	s := New(server.Client())
	models, err := s.LookupModels(context.Background(), server.URL, map[string]string{"X-Test": "value"})
	if err != nil {
		t.Fatal(err)
	}

	want := []providers.Model{{ID: "alpha", DisplayName: "alpha"}, {ID: "beta", DisplayName: "beta"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}
