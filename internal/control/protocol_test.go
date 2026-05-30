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
