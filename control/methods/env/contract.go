package env

// The env method contract: method names, typed request builders, and param/result
// decoders. The host-side sandbox client (sender) and the handlers in this package
// share these so the wire shape lives in exactly one place.

import (
	"encoding/json"
	"errors"
	"strings"

	"petris.dev/toby/control"
)

// Control method names for the env capability.
const (
	MethodGet = "env.get"
	MethodSet = "env.set"
)

func NewGetRequest(id int64) ([]byte, error) {
	return control.NewRequest(id, MethodGet, nil)
}

func NewSetRequest(id int64, params SetParams) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return control.NewRequest(id, MethodSet, data)
}

func DecodeSetParams(raw json.RawMessage) (SetParams, error) {
	params, err := control.DecodeParams[SetParams](raw)
	if err != nil {
		return SetParams{}, err
	}
	if params.Name == "" {
		return SetParams{}, errors.New("name is required")
	}
	if strings.ContainsAny(params.Name, "=\x00") {
		return SetParams{}, errors.New("invalid environment variable name")
	}
	if strings.ContainsRune(params.Value, 0) {
		return SetParams{}, errors.New("invalid environment variable value")
	}
	return params, nil
}

func DecodeGetResult(result any) (GetResult, error) {
	decoded, err := control.DecodeResult[GetResult](result)
	if err != nil {
		return GetResult{}, err
	}
	if decoded.Environment == nil {
		decoded.Environment = map[string]string{}
	}
	return decoded, nil
}
