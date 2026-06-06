package git

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/control"
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
			build:      func() ([]byte, error) { return NewCommitRequest(42, "repo", "message", false) },
			wantMethod: MethodCommit,
			wantParams: map[string]any{"repository": "repo", "message": "message"},
		},
		{
			name:       "fetch",
			build:      func() ([]byte, error) { return NewFetchRequest(42, "repo") },
			wantMethod: MethodFetch,
			wantParams: map[string]any{"repository": "repo"},
		},
		{
			name:       "push",
			build:      func() ([]byte, error) { return NewPushRequest(42, "repo", "main", "", false) },
			wantMethod: MethodPush,
			wantParams: map[string]any{"repository": "repo", "branch": "main"},
		},
		{
			name:       "rebase continue",
			build:      func() ([]byte, error) { return NewRebaseRequest(42, "repo", "", true, false) },
			wantMethod: MethodRebase,
			wantParams: map[string]any{"repository": "repo", "continue": true},
		},
		{
			name:       "tag",
			build:      func() ([]byte, error) { return NewTagRequest(42, "repo", "v1", "release", "") },
			wantMethod: MethodTag,
			wantParams: map[string]any{"repository": "repo", "tag": "v1", "message": "release"},
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

func TestDecodeGitParamsValidation(t *testing.T) {
	if _, err := DecodeRepositoryParams(nil); err == nil || !strings.Contains(err.Error(), "missing params") {
		t.Fatalf("repository missing err = %v", err)
	}
	if _, err := DecodeRepositoryParams(json.RawMessage(`{"repository":""}`)); err == nil || !strings.Contains(err.Error(), "repository is required") {
		t.Fatalf("repository err = %v", err)
	}
	if params, err := DecodeRepositoryParams(json.RawMessage(`{"repository":"repo"}`)); err != nil || params.Repository != "repo" {
		t.Fatalf("repository params = %#v, err = %v", params, err)
	}
	if _, err := DecodeCommitParams(json.RawMessage(`{"repository":"repo"}`)); err == nil || !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("commit err = %v", err)
	}
	if _, err := DecodePushParams(json.RawMessage(`{"repository":"repo"}`)); err == nil || !strings.Contains(err.Error(), "branch is required") {
		t.Fatalf("push err = %v", err)
	}
	if _, err := DecodeTagParams(json.RawMessage(`{"repository":"repo","tag":"v1"}`)); err == nil || !strings.Contains(err.Error(), "message is required") {
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
			_, err := DecodeRebaseParams(json.RawMessage(tt.raw))
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

func TestDecodeGitResultRoundTrips(t *testing.T) {
	response := control.ResponseOK(json.RawMessage(`1`), Result{Repository: "repo", Stdout: "ok"})
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	result, err := DecodeResult(decoded.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != "repo" || result.Stdout != "ok" {
		t.Fatalf("git result = %#v", result)
	}
}
