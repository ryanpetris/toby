package files

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeFileParamsValidation(t *testing.T) {
	tests := []struct {
		name    string
		decode  func(json.RawMessage) error
		raw     json.RawMessage
		wantErr string
	}{
		{name: "create missing params", decode: func(raw json.RawMessage) error { _, err := DecodeCreateParams(raw); return err }, wantErr: "missing params"},
		{name: "create path", decode: func(raw json.RawMessage) error { _, err := DecodeCreateParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "delete path", decode: func(raw json.RawMessage) error { _, err := DecodeDeleteParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "mkdir path", decode: func(raw json.RawMessage) error { _, err := DecodeMkdirParams(raw); return err }, raw: json.RawMessage(`{}`), wantErr: "path is required"},
		{name: "symlink target", decode: func(raw json.RawMessage) error { _, err := DecodeSymlinkParams(raw); return err }, raw: json.RawMessage(`{"path":"link"}`), wantErr: "target is required"},
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

func TestDecodeFileCreateUIDGIDNullDefaultsRoot(t *testing.T) {
	params, err := DecodeCreateParams(json.RawMessage(`{"path":"file","uid":null,"gid":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.UID != 0 || params.GID != 0 {
		t.Fatalf("file owner = %d:%d, want root", params.UID, params.GID)
	}
}
