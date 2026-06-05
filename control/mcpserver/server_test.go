package mcpserver

import (
	"context"
	"embed"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed testdata/sample.md
var testDocs embed.FS

type testService struct {
	tools     []Tool
	resources []Resource
}

func (s testService) Tools() []Tool { return s.tools }

func (s testService) Resources() []Resource { return s.resources }

func TestNewRunnerRejectsDuplicateTools(t *testing.T) {
	_, err := NewRunner(RunnerParams{Services: []Service{
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate tool to fail")
	}
}

func TestNewRunnerRejectsDuplicateResources(t *testing.T) {
	_, err := NewRunner(RunnerParams{Services: []Service{
		testService{resources: []Resource{{URI: "toby://test", Name: "test", Text: staticResourceText("one")}}},
		testService{resources: []Resource{{URI: "toby://test", Name: "test-again", Text: staticResourceText("two")}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate resource to fail")
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

func TestNewRunnerValidatesResourceDefinitions(t *testing.T) {
	tests := []struct {
		name     string
		resource Resource
	}{
		{name: "empty uri", resource: Resource{Name: "test", Text: staticResourceText("text")}},
		{name: "empty name", resource: Resource{URI: "toby://test", Text: staticResourceText("text")}},
		{name: "missing source", resource: Resource{URI: "toby://test", Name: "test"}},
		{name: "partial static", resource: Resource{URI: "toby://test", Name: "test", FilePath: "test.md"}},
		{name: "multiple sources", resource: Resource{URI: "toby://test", Name: "test", FS: testDocs, FilePath: "testdata/sample.md", Text: staticResourceText("text")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRunner(RunnerParams{Services: []Service{testService{resources: []Resource{tt.resource}}}}); err == nil {
				t.Fatal("expected invalid resource to fail")
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

func TestStaticResourceReadsEmbeddedFile(t *testing.T) {
	resource := Resource{URI: "toby://docs/sample", Name: "toby.docs.sample", FS: testDocs, FilePath: "testdata/sample.md"}
	text, err := resource.Read(context.Background(), &Session{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "# Sample") {
		t.Fatalf("sample resource text = %q", text)
	}
}

func noopRegister(*mcp.Server, *Session) {}

func staticResourceText(text string) func(context.Context, *Session) (string, error) {
	return func(context.Context, *Session) (string, error) { return text, nil }
}
