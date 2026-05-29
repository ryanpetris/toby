package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testService struct {
	tools []Tool
}

func (s testService) Tools() []Tool { return s.tools }

func TestRunRequiresTobySandbox(t *testing.T) {
	err := Run(context.Background(), filepath.Join(t.TempDir(), "missing-control"))
	if err == nil {
		t.Fatal("expected missing control path to fail")
	}
	if !strings.Contains(err.Error(), "inside a Toby sandbox") {
		t.Fatalf("err = %v, want sandbox error", err)
	}
}

func TestNewRunnerRejectsDuplicateTools(t *testing.T) {
	_, err := NewRunner(RunnerParams{Services: []Service{
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate tool to fail")
	}
}

func noopRegister(*mcp.Server, *Server) {}
