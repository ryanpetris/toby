package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeCommandRunParamsAllowsForegroundEmptyArgv(t *testing.T) {
	raw, err := json.Marshal(CommandRunParams{CommandID: "id", Foreground: true})
	if err != nil {
		t.Fatal(err)
	}
	params, err := DecodeCommandRunParams(raw)
	if err != nil {
		t.Fatal(err)
	}
	if params.CommandID != "id" || !params.Foreground || len(params.Argv) != 0 {
		t.Fatalf("params = %#v", params)
	}
}

func TestDecodeCommandRunParamsRejectsBackgroundEmptyArgv(t *testing.T) {
	raw, err := json.Marshal(CommandRunParams{CommandID: "id"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeCommandRunParams(raw)
	if err == nil || !strings.Contains(err.Error(), "argv is required for background commands") {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultEndpointUsesControlHost(t *testing.T) {
	t.Setenv(EnvControlHost, "127.0.0.1:1234")
	t.Setenv(EnvControlToken, "secret")
	endpoint, err := DefaultEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "127.0.0.1:1234" || endpoint.Token != "secret" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
	if endpoint.ControlURL() != "ws://127.0.0.1:1234/control" {
		t.Fatalf("control URL = %q", endpoint.ControlURL())
	}
	if endpoint.BinaryURL() != "http://127.0.0.1:1234/binary" {
		t.Fatalf("binary URL = %q", endpoint.BinaryURL())
	}
	if endpoint.ProxyBaseURL("local/proxy") != "http://127.0.0.1:1234/proxy/local%2Fproxy" {
		t.Fatalf("proxy URL = %q", endpoint.ProxyBaseURL("local/proxy"))
	}
}
