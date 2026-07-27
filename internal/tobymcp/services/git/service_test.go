package gitservice

// Verifies MCP result shaping and fail-closed live capability handling.

import (
	"testing"

	"petris.dev/toby/internal/hostaction/methods/git"
)

func TestGitToolResultMarksNonzeroExitAsError(t *testing.T) {
	if result := gitToolResult(git.Result{}); result != nil {
		t.Fatalf("zero exit result = %#v", result)
	}
	result := gitToolResult(git.Result{ExitCode: 1})
	if result == nil || !result.IsError {
		t.Fatalf("nonzero exit result = %#v", result)
	}
}

func TestGitHandlerRejectsMissingLiveCapability(t *testing.T) {
	_, _, err := (handler{}).run(func() (git.Result, error) {
		t.Fatal("missing Git capability invoked operation")
		return git.Result{}, nil
	})
	if err == nil {
		t.Fatal("Git handler accepted a missing live launch capability")
	}
}
