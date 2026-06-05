package env

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"petris.dev/toby/control"
)

func TestDecodeEnvironmentSetParamsValidation(t *testing.T) {
	if params, err := DecodeSetParams(json.RawMessage(`{"name":"FOO","value":""}`)); err != nil || params.Name != "FOO" || params.Value != "" {
		t.Fatalf("unset params = %#v, err = %v", params, err)
	}
	if _, err := DecodeSetParams(json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("missing name err = %v", err)
	}
	for _, tt := range []struct {
		raw     string
		wantErr string
	}{
		{raw: `{"name":"BAD=NAME","value":"x"}`, wantErr: "invalid environment variable name"},
		{raw: `{"name":"BAD\u0000NAME","value":"x"}`, wantErr: "invalid environment variable name"},
		{raw: `{"name":"FOO","value":"BAD\u0000VALUE"}`, wantErr: "invalid environment variable value"},
	} {
		_, err := DecodeSetParams(json.RawMessage(tt.raw))
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
		}
	}
}

func TestDecodeEnvironmentGetResult(t *testing.T) {
	response := control.ResponseOK(json.RawMessage(`1`), GetResult{Environment: map[string]string{"FOO": "bar"}})
	decoded, err := control.DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeGetResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Environment["FOO"] != "bar" {
		t.Fatalf("environment = %#v", result.Environment)
	}
	result, err = DecodeGetResult(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Environment == nil || len(result.Environment) != 0 {
		t.Fatalf("empty environment = %#v", result.Environment)
	}
}
