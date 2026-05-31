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

func TestNewRunnerValidatesToolDefinitions(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{name: "empty name", tool: Tool{Register: noopRegister}},
		{name: "nil register", tool: Tool{Name: "test.tool"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRunner(RunnerParams{Services: []Service{testService{tools: []Tool{tt.tool}}}}); err == nil {
				t.Fatal("expected invalid tool to fail")
			}
		})
	}
}

func TestNewRunnerSkipsNilServices(t *testing.T) {
	runner, err := NewRunner(RunnerParams{Services: []Service{nil, testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.tools) != 1 || runner.tools[0].Name != "test.tool" {
		t.Fatalf("tools = %#v", runner.tools)
	}
}

func TestGitToolResultMarksNonzeroExitAsError(t *testing.T) {
	if result := gitToolResult(GitOutput{}); result != nil {
		t.Fatalf("zero exit result = %#v", result)
	}
	result := gitToolResult(GitOutput{ExitCode: 1})
	if result == nil || !result.IsError {
		t.Fatalf("nonzero exit result = %#v", result)
	}
}

func noopRegister(*mcp.Server, *Server) {}
