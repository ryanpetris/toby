package control

// Generic decoding of JSON-RPC payloads: capability packages define their own
// typed params/results and reuse these helpers to unmarshal them. The helpers are
// method-agnostic, so they live with the transport rather than any one capability.

import (
	"encoding/json"
	"errors"
)

// EmptyResult is the result payload of a method that returns no data.
type EmptyResult struct{}

// DecodeParams unmarshals required request params into T, erroring when absent.
func DecodeParams[T any](raw json.RawMessage) (T, error) {
	var dest T
	if len(raw) == 0 {
		return dest, errors.New("missing params")
	}
	if err := json.Unmarshal(raw, &dest); err != nil {
		return dest, err
	}
	return dest, nil
}

// DecodeResult re-decodes a response result value into T.
func DecodeResult[T any](result any) (T, error) {
	var dest T
	if result == nil {
		return dest, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return dest, err
	}
	if err := json.Unmarshal(data, &dest); err != nil {
		return dest, err
	}
	return dest, nil
}
