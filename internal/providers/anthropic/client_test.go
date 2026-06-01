package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNewClientRequiresHTTPClient(t *testing.T) {
	if _, err := NewClient(nil, "https://example.test", nil); err == nil {
		t.Fatal("expected nil HTTP client to fail")
	}
}

func TestModelsPaginates(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Fatalf("Accept = %q, want application/json", accept)
		}
		if userAgent := r.Header.Get("User-Agent"); userAgent != UserAgent {
			t.Fatalf("User-Agent = %q, want %q", userAgent, UserAgent)
		}
		if header := r.Header.Get("X-Test"); header != "value" {
			t.Fatalf("X-Test = %q, want value", header)
		}
		switch requestCount {
		case 1:
			if after := r.URL.Query().Get("after_id"); after != "" {
				t.Fatalf("first after_id = %q, want empty", after)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"alpha","display_name":"Alpha"},{"id":""}],"has_more":true,"last_id":"alpha"}`))
		case 2:
			if after := r.URL.Query().Get("after_id"); after != "alpha" {
				t.Fatalf("second after_id = %q, want alpha", after)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"beta"}],"has_more":false,"last_id":"beta"}`))
		default:
			t.Fatalf("unexpected request %d", requestCount)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.Client(), server.URL+"/v1", map[string]string{"X-Test": "value"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "alpha", DisplayName: "Alpha"}, {ID: "beta"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}
