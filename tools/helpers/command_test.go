package helpers_test

import (
	"context"
	"errors"
	"testing"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
	"petris.dev/toby/tools/helpers"
)

func TestCommandExists(t *testing.T) {
	sandbox := &fake.Sandbox{ExecFunc: func(context.Context, []string, sandboxapi.ExecOptions) (int, error) {
		return 0, nil
	}}
	exists, err := helpers.CommandExists(context.Background(), sandbox.Exec, sandboxapi.ExecOptions{HideOutput: true}, "demo")
	if err != nil || !exists {
		t.Fatalf("exists = %v, err = %v", exists, err)
	}
}

func TestCommandExistsTreatsExitCodeAsMissing(t *testing.T) {
	sandbox := &fake.Sandbox{ExecFunc: func(context.Context, []string, sandboxapi.ExecOptions) (int, error) {
		return 1, exitcode.Code(1)
	}}
	exists, err := helpers.CommandExists(context.Background(), sandbox.Exec, sandboxapi.ExecOptions{HideOutput: true}, "missing")
	if err != nil || exists {
		t.Fatalf("exists = %v, err = %v", exists, err)
	}
}

func TestCommandExistsReturnsExecutionErrors(t *testing.T) {
	sentinel := errors.New("boom")
	sandbox := &fake.Sandbox{ExecFunc: func(context.Context, []string, sandboxapi.ExecOptions) (int, error) {
		return 1, sentinel
	}}
	if _, err := helpers.CommandExists(context.Background(), sandbox.Exec, sandboxapi.ExecOptions{HideOutput: true}, "demo"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}
