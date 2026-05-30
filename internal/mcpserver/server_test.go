package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testService struct {
	tools []Tool
}

func (s testService) Tools() []Tool { return s.tools }

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
