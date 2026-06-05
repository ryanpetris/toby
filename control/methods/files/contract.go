package files

// The file method contract: method names and param decoders. The host-side
// sandbox client (sender) and the handlers in this package share these so the wire
// shape lives in exactly one place.

import (
	"encoding/json"
	"errors"

	"petris.dev/toby/control"
)

// Control method names for the file capability.
const (
	MethodCreate  = "file.create"
	MethodDelete  = "file.delete"
	MethodMkdir   = "file.mkdir"
	MethodSymlink = "file.symlink"
)

func DecodeCreateParams(raw json.RawMessage) (CreateParams, error) {
	params, err := control.DecodeParams[CreateParams](raw)
	if err != nil {
		return CreateParams{}, err
	}
	if params.Path == "" {
		return CreateParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeDeleteParams(raw json.RawMessage) (DeleteParams, error) {
	params, err := control.DecodeParams[DeleteParams](raw)
	if err != nil {
		return DeleteParams{}, err
	}
	if params.Path == "" {
		return DeleteParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeMkdirParams(raw json.RawMessage) (MkdirParams, error) {
	params, err := control.DecodeParams[MkdirParams](raw)
	if err != nil {
		return MkdirParams{}, err
	}
	if params.Path == "" {
		return MkdirParams{}, errors.New("path is required")
	}
	return params, nil
}

func DecodeSymlinkParams(raw json.RawMessage) (SymlinkParams, error) {
	params, err := control.DecodeParams[SymlinkParams](raw)
	if err != nil {
		return SymlinkParams{}, err
	}
	if params.Path == "" {
		return SymlinkParams{}, errors.New("path is required")
	}
	if params.Target == "" {
		return SymlinkParams{}, errors.New("target is required")
	}
	return params, nil
}
