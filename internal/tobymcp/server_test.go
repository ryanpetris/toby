package tobymcp

// Validates contributor composition, per-connection isolation, and transport
// lifecycle behavior.

import (
	"context"
	"embed"
	"errors"
	"strings"
	"testing"
	"time"

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
	_, err := NewRunner(RunnerParams{Contributors: []Contributor{
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
		testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate tool to fail")
	}
}

func TestNewRunnerRejectsDuplicateResources(t *testing.T) {
	_, err := NewRunner(RunnerParams{Contributors: []Contributor{
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
			if _, err := NewRunner(RunnerParams{Contributors: []Contributor{testService{tools: []Tool{tt.tool}}}}); err == nil {
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
			if _, err := NewRunner(RunnerParams{Contributors: []Contributor{testService{resources: []Resource{tt.resource}}}}); err == nil {
				t.Fatal("expected invalid resource to fail")
			}
		})
	}
}

func TestNewRunnerSkipsNilContributors(t *testing.T) {
	runner, err := NewRunner(RunnerParams{Contributors: []Contributor{nil, testService{tools: []Tool{{Name: "test.tool", Register: noopRegister}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.tools) != 1 || runner.tools[0].Name != "test.tool" {
		t.Fatalf("tools = %#v", runner.tools)
	}
}

func TestStaticResourceReadsEmbeddedFile(t *testing.T) {
	resource := Resource{URI: "toby://docs/sample", Name: "toby.docs.sample", FS: testDocs, FilePath: "testdata/sample.md"}
	text, err := resource.Read(t.Context(), &Session{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "# Sample") {
		t.Fatalf("sample resource text = %q", text)
	}
}

func TestRunnerCreatesIsolatedSessions(t *testing.T) {
	var sessions []*Session
	resource := Resource{
		URI:  "toby://test",
		Name: "test",
		Text: staticResourceText("text"),
	}
	runner, err := NewRunner(RunnerParams{Contributors: []Contributor{testService{
		tools: []Tool{{
			Name: "test.tool",
			Register: func(_ *mcp.Server, session *Session) {
				sessions = append(sessions, session)
			},
		}},
		resources: []Resource{resource},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := validSessionSnapshot()
	snapshot.Runtime.Name = "first"
	if server := runner.server(nil, snapshot); server == nil {
		t.Fatal("first server is nil")
	}
	snapshot.Runtime.Name = "second"
	if server := runner.server(nil, snapshot); server == nil {
		t.Fatal("second server is nil")
	}

	if len(sessions) != 2 {
		t.Fatalf("registered sessions = %d, want 2", len(sessions))
	}
	if sessions[0] == sessions[1] {
		t.Fatal("server connections share one Session")
	}
	if got := sessions[0].Snapshot.Runtime.Name; got != "first" {
		t.Fatalf("first session runtime name = %q", got)
	}
	if got := sessions[1].Snapshot.Runtime.Name; got != "second" {
		t.Fatalf("second session runtime name = %q", got)
	}

	sessions[0].Resources[0].Name = "changed"
	if got := sessions[1].Resources[0].Name; got != resource.Name {
		t.Fatalf("second session resource name = %q, want %q", got, resource.Name)
	}
}

func TestRunnerServeWaitsForClientClose(t *testing.T) {
	runner, err := NewRunner(RunnerParams{})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- runner.Serve(
			t.Context(),
			nil,
			validSessionSnapshot(),
			serverTransport,
		)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientSession.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the client closed")
	}
}

func TestRunnerServeClosesSessionWhenContextEnds(t *testing.T) {
	runner, err := NewRunner(RunnerParams{})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, _ := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- runner.Serve(
			ctx,
			nil,
			validSessionSnapshot(),
			serverTransport,
		)
	}()

	cancel()

	select {
	case err := <-serveResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve() error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestRunnerServeValidatesInputs(t *testing.T) {
	runner, err := NewRunner(RunnerParams{})
	if err != nil {
		t.Fatal(err)
	}
	transport, _ := mcp.NewInMemoryTransports()
	var nilContext context.Context

	if err := runner.Serve(
		nilContext,
		nil,
		validSessionSnapshot(),
		transport,
	); err == nil {
		t.Fatal("Serve accepted a nil context")
	}
	if err := runner.Serve(
		t.Context(),
		nil,
		validSessionSnapshot(),
		nil,
	); err == nil {
		t.Fatal("Serve accepted a nil transport")
	}
	if err := runner.Serve(
		t.Context(),
		nil,
		SessionSnapshot{},
		transport,
	); err == nil {
		t.Fatal("Serve accepted an invalid session snapshot")
	}

	var nilRunner *Runner
	if err := nilRunner.Serve(
		t.Context(),
		nil,
		validSessionSnapshot(),
		transport,
	); err == nil {
		t.Fatal("Serve accepted a nil runner")
	}
}

func noopRegister(*mcp.Server, *Session) {}

func staticResourceText(text string) func(context.Context, *Session) (string, error) {
	return func(context.Context, *Session) (string, error) { return text, nil }
}
