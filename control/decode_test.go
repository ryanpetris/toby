package control

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponsesEncodeDecodeAndCloneID(t *testing.T) {
	id := json.RawMessage(`1`)
	response := ResponseOK(id, EmptyResult{})
	id[0] = '2' // mutate caller's id; the response must keep its own clone
	if !bytes.HasSuffix(response, []byte("\n")) {
		t.Fatalf("response is not newline terminated: %q", response)
	}
	decoded, err := DecodeResponse(bytes.TrimSpace(response))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded.ID) != "1" {
		t.Fatalf("id = %s, want 1 (clone must be unaffected)", decoded.ID)
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

func TestDecodeParamsRequiresParams(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	if _, err := DecodeParams[payload](nil); err == nil {
		t.Fatal("expected missing params to error")
	}
	got, err := DecodeParams[payload](json.RawMessage(`{"name":"toby"}`))
	if err != nil || got.Name != "toby" {
		t.Fatalf("params = %#v, err = %v", got, err)
	}
}

func TestDecodeResultRoundTripsAndAllowsNil(t *testing.T) {
	type payload struct {
		Count int `json:"count"`
	}
	got, err := DecodeResult[payload](map[string]any{"count": 3})
	if err != nil || got.Count != 3 {
		t.Fatalf("result = %#v, err = %v", got, err)
	}
	empty, err := DecodeResult[EmptyResult](nil)
	if err != nil {
		t.Fatalf("nil result should decode to zero value: %v", err)
	}
	_ = empty
}
