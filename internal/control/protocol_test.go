package control

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNewGitRequestsEncodeJSONRPCEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		build      func() ([]byte, error)
		wantMethod string
		wantParams map[string]any
	}{
		{
			name:       "commit",
			build:      func() ([]byte, error) { return NewGitCommitRequest(42, "repo", "message", false) },
			wantMethod: MethodGitCommit,
			wantParams: map[string]any{"repository": "repo", "message": "message"},
		},
		{
			name:       "fetch",
			build:      func() ([]byte, error) { return NewGitFetchRequest(42, "repo") },
			wantMethod: MethodGitFetch,
			wantParams: map[string]any{"repository": "repo"},
		},
		{
			name:       "push",
			build:      func() ([]byte, error) { return NewGitPushRequest(42, "repo", "main", "", false) },
			wantMethod: MethodGitPush,
			wantParams: map[string]any{"repository": "repo", "branch": "main"},
		},
		{
			name:       "rebase continue",
			build:      func() ([]byte, error) { return NewGitRebaseRequest(42, "repo", "", true, false) },
			wantMethod: MethodGitRebase,
			wantParams: map[string]any{"repository": "repo", "continue": true},
		},
		{
			name:       "tag",
			build:      func() ([]byte, error) { return NewGitTagRequest(42, "repo", "v1", "release", "") },
			wantMethod: MethodGitTag,
			wantParams: map[string]any{"repository": "repo", "tag": "v1", "message": "release"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.build()
			if err != nil {
				t.Fatal(err)
			}
			req, err := DecodeRequest(data)
			if err != nil {
				t.Fatal(err)
			}
			if req.JSONRPC != JSONRPCVersion || string(req.ID) != "42" || req.Method != tt.wantMethod {
				t.Fatalf("request = %#v", req)
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

func TestDecodeRequestValidatesEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "valid", data: `{"jsonrpc":"2.0","id":1,"method":"git.fetch"}`},
		{name: "malformed", data: `{`, wantErr: "parse request"},
		{name: "wrong version", data: `{"jsonrpc":"1.0","id":1,"method":"git.fetch"}`, wantErr: "invalid JSON-RPC request"},
		{name: "missing id", data: `{"jsonrpc":"2.0","method":"git.fetch"}`, wantErr: "invalid JSON-RPC request"},
		{name: "empty method", data: `{"jsonrpc":"2.0","id":1}`, wantErr: "invalid JSON-RPC request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeGitParamsValidation(t *testing.T) {
	if _, err := DecodeGitRepositoryParams(nil); err == nil || !strings.Contains(err.Error(), "missing params") {
		t.Fatalf("repository missing err = %v", err)
	}
	if _, err := DecodeGitRepositoryParams(json.RawMessage(`{"repository":""}`)); err == nil || !strings.Contains(err.Error(), "repository is required") {
		t.Fatalf("repository err = %v", err)
	}
	if params, err := DecodeGitRepositoryParams(json.RawMessage(`{"repository":"repo"}`)); err != nil || params.Repository != "repo" {
		t.Fatalf("repository params = %#v, err = %v", params, err)
	}
	if _, err := DecodeGitCommitParams(json.RawMessage(`{"repository":"repo"}`)); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("commit err = %v", err)
	}
	if _, err := DecodeGitPushParams(json.RawMessage(`{"repository":"repo"}`)); err == nil || !strings.Contains(err.Error(), "branch is required") {
		t.Fatalf("push err = %v", err)
	}
	if _, err := DecodeGitTagParams(json.RawMessage(`{"repository":"repo","tag":"v1"}`)); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("tag err = %v", err)
	}
}

func TestDecodeGitRebaseParamsRequiresExactlyOneMode(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "base", raw: `{"repository":"repo","base":"main"}`},
		{name: "continue", raw: `{"repository":"repo","continue":true}`},
		{name: "abort", raw: `{"repository":"repo","abort":true}`},
		{name: "none", raw: `{"repository":"repo"}`, wantErr: true},
		{name: "multiple", raw: `{"repository":"repo","base":"main","continue":true}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeGitRebaseParams(json.RawMessage(tt.raw))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "exactly one of base, continue, or abort is required") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeFileAndCommandParamsValidation(t *testing.T) {
	tests := []struct {
		name    string
		decode  func(json.RawMessage) error
		raw     json.RawMessage
		wantErr string
	}{
		{name: "file create missing params", decode: func(raw json.RawMessage) error { _, err := DecodeFileCreateParams(raw); return err }, wantErr: "missing params"},
		{name: "file create path", decode: func(raw json.RawMessage) error { _, err := DecodeFileCreateParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "file delete path", decode: func(raw json.RawMessage) error { _, err := DecodeFileDeleteParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "file mkdir path", decode: func(raw json.RawMessage) error { _, err := DecodeFileMkdirParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "file symlink target", decode: func(raw json.RawMessage) error { _, err := DecodeFileSymlinkParams(raw); return err }, raw: json.RawMessage(`{"path":"link"}`), wantErr: "target is required"},
		{name: "command run id", decode: func(raw json.RawMessage) error { _, err := DecodeCommandRunParams(raw); return err }, raw: json.RawMessage(`{"argv":["true"]}`), wantErr: "command_id is required"},
		{name: "command exit id", decode: func(raw json.RawMessage) error { _, err := DecodeCommandExitParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "command_id is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decode(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestResponsesEncodeDecodeAndCloneID(t *testing.T) {
	id := json.RawMessage(`1`)
	response := ResponseOK(id, GitResult{Repository: "repo", Stdout: "ok"})
	id[0] = '2'
	if !bytes.HasSuffix(response, []byte("\n")) {
		t.Fatalf("response is not newline terminated: %q", response)
	}
	decoded, err := DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.ID) != "1" {
		t.Fatalf("id = %s", decoded.ID)
	}
	result, err := DecodeGitResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != "repo" || result.Stdout != "ok" {
		t.Fatalf("git result = %#v", result)
	}
	if _, err := DecodeEmptyResult(nil); err != nil {
		t.Fatal(err)
	}

	errorResponse := ResponseError(nil, CodeInvalidParams, "bad", map[string]any{"field": "x"})
	decoded, err = DecodeResponse(bytes.TrimSpace(errorResponse))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.ID) != "null" || decoded.Error == nil || decoded.Error.Code != CodeInvalidParams || decoded.Error.Message != "bad" {
		t.Fatalf("error response = %#v", decoded)
	}
}

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

func TestDefaultEndpointValidationAndURLHelpers(t *testing.T) {
	t.Setenv(EnvControlHost, "")
	if _, err := DefaultEndpoint(); err == nil || !strings.Contains(err.Error(), EnvControlHost+" is required") {
		t.Fatalf("missing endpoint err = %v", err)
	}
	t.Setenv(EnvControlHost, "localhost")
	if _, err := DefaultEndpoint(); err == nil || !strings.Contains(err.Error(), "invalid "+EnvControlHost) {
		t.Fatalf("invalid endpoint err = %v", err)
	}
	t.Setenv(EnvControlHost, " [::1]:1234 ")
	t.Setenv(EnvControlToken, "token")
	endpoint, err := DefaultEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "[::1]:1234" || endpoint.ControlURL() != "ws://[::1]:1234/control" || endpoint.BinaryURL() != "http://[::1]:1234/binary" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
	if endpoint.ProxyBaseURL("space id/slash") != "http://[::1]:1234/proxy/space%20id%2Fslash" {
		t.Fatalf("proxy URL = %q", endpoint.ProxyBaseURL("space id/slash"))
	}
	if err := validateHostPort(":1234"); err == nil {
		t.Fatal("expected empty host to fail")
	}
	if err := validateHostPort("localhost:"); err == nil {
		t.Fatal("expected empty port to fail")
	}
}
