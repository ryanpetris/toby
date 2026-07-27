package openai

// Tests bounded OpenAI-compatible model discovery.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/providers"
)

func TestGetModelsRequiresHTTPClient(t *testing.T) {
	s := newService(nil, nil)
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

	s := newService(server.Client(), nil)
	models, err := s.LookupModels(context.Background(), server.URL, map[string]string{"X-Test": "value"})
	if err != nil {
		t.Fatal(err)
	}

	want := []providers.Model{{ID: "alpha", DisplayName: "alpha"}, {ID: "beta", DisplayName: "beta"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
}

func TestGetModelsDoesNotExposeProviderErrorBody(t *testing.T) {
	const canary = "provider-secret-canary"

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(canary))
	}))
	t.Cleanup(server.Close)

	_, err := newService(server.Client(), nil).LookupModels(
		context.Background(),
		server.URL,
		nil,
	)
	if err == nil {
		t.Fatal("provider error response succeeded")
	}
	if strings.Contains(err.Error(), canary) ||
		strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error exposed protected provider detail: %v", err)
	}
}

func TestGetModelsRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = writer.Write([]byte(`{"data":"`))
		_, _ = writer.Write([]byte(
			strings.Repeat("x", maxModelsResponseBytes),
		))
		_, _ = writer.Write([]byte(`"}`))
	}))
	t.Cleanup(server.Close)

	if _, err := newService(server.Client(), nil).LookupModels(
		context.Background(),
		server.URL,
		nil,
	); err == nil {
		t.Fatal("oversized provider response succeeded")
	}
}
