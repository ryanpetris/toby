package openai

import "testing"

func TestNewClientRequiresHTTPClient(t *testing.T) {
	if _, err := NewClient(nil, "https://example.test", "", nil); err == nil {
		t.Fatal("expected nil HTTP client to fail")
	}
}
