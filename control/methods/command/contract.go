package command

// The command method contract: method names and param decoders. The capability
// in this package handles command.run and emits command.exit; the host registers
// a command.exit handler. They share this contract so the wire shape lives in one
// place.

import (
	"encoding/json"
	"errors"

	"petris.dev/toby/control"
)

// Control method names for the command capability.
const (
	MethodRun  = "command.run"
	MethodExit = "command.exit"
)

func DecodeRunParams(raw json.RawMessage) (RunParams, error) {
	params, err := control.DecodeParams[RunParams](raw)
	if err != nil {
		return RunParams{}, err
	}
	if params.CommandID == "" {
		return RunParams{}, errors.New("command_id is required")
	}
	if len(params.Argv) == 0 && !params.Foreground {
		return RunParams{}, errors.New("argv is required for background commands")
	}
	return params, nil
}

func DecodeExitParams(raw json.RawMessage) (ExitParams, error) {
	params, err := control.DecodeParams[ExitParams](raw)
	if err != nil {
		return ExitParams{}, err
	}
	if params.CommandID == "" {
		return ExitParams{}, errors.New("command_id is required")
	}
	return params, nil
}
