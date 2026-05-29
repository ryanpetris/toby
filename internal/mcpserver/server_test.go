package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresTobySandbox(t *testing.T) {
	err := Run(context.Background(), filepath.Join(t.TempDir(), "missing-control"))
	if err == nil {
		t.Fatal("expected missing control path to fail")
	}
	if !strings.Contains(err.Error(), "inside a Toby sandbox") {
		t.Fatalf("err = %v, want sandbox error", err)
	}
}
