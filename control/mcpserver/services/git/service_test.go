package gitservice

import (
	"testing"

	"petris.dev/toby/control/methods/git"
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
