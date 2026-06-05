package kit

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGetJSONSetsHeadersAndDecodesResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept = %q", req.Header.Get("Accept"))
		}
		if req.Header.Get("User-Agent") == "" {
			t.Fatal("User-Agent was not set")
		}
		return httpResponse(http.StatusOK, `{"name":"toby"}`), nil
	})}
	var got struct {
		Name string `json:"name"`
	}

	if err := GetJSON(context.Background(), client, "https://example.invalid/release", "application/json", &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "toby" {
		t.Fatalf("decoded = %#v", got)
	}
}

func TestGetJSONReportsNon2xxBody(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return httpResponse(http.StatusBadGateway, "upstream failed"), nil
	})}
	var got struct{}

	err := GetJSON(context.Background(), client, "https://example.invalid/release", "", &got)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "upstream failed") {
		t.Fatalf("err = %v", err)
	}
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
