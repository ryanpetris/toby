package env

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/control"
)

func TestNewEnvironmentRequestsEncodeJSONRPCEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		build      func() ([]byte, error)
		wantMethod string
		wantParams map[string]any
	}{
		{
			name:       "get",
			build:      func() ([]byte, error) { return NewGetRequest(42) },
			wantMethod: MethodGet,
		},
		{
			name: "set",
			build: func() ([]byte, error) {
				return NewSetRequest(42, SetParams{Name: "FOO", Value: "bar"})
			},
			wantMethod: MethodSet,
			wantParams: map[string]any{"name": "FOO", "value": "bar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.build()
			if err != nil {
				t.Fatal(err)
			}
			req, err := control.DecodeRequest(data)
			if err != nil {
				t.Fatal(err)
			}
			if req.JSONRPC != control.JSONRPCVersion || string(req.ID) != "42" || req.Method != tt.wantMethod {
				t.Fatalf("request = %#v", req)
			}
			if tt.wantParams == nil {
				if len(req.Params) != 0 {
					t.Fatalf("params = %s, want none", req.Params)
				}
				return
			}
			var params map[string]any
			if err := json.Unmarshal(req.Params, &params); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(params, tt.wantParams) {
				t.Fatalf("params = %#v, want %#v", params, tt.wantParams)
			}
		})
	}
}

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
