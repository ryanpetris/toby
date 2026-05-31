package sandbox

import (
	"errors"
	"testing"

	"petris.dev/toby/internal/control"
)

func TestCommandExitResultReturnsExitCodeErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 7})
	var coded interface{ ExitCode() int }
	if code != 7 || !errors.As(err, &coded) || coded.ExitCode() != 7 || err.Error() != "" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultReturnsCommandErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 127, Error: "not found"})
	var coded interface{ ExitCode() int }
	if code != 127 || !errors.As(err, &coded) || coded.ExitCode() != 127 || err.Error() != "not found" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultSuccess(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{})
	if code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}
