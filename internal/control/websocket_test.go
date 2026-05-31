package control

import (
	"net/http"
	"testing"
)

func TestHeaderContainsMatchesCommaSeparatedValues(t *testing.T) {
	header := http.Header{}
	header.Add("Connection", "keep-alive, Upgrade")
	header.Add("Connection", "other")
	if !headerContains(header, "Connection", "upgrade") {
		t.Fatal("expected header to contain upgrade")
	}
	if headerContains(header, "Connection", "websocket") {
		t.Fatal("did not expect websocket token")
	}
}

func TestWebsocketAcceptMatchesRFCExample(t *testing.T) {
	if got, want := websocketAccept("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Fatalf("websocketAccept = %q, want %q", got, want)
	}
}
