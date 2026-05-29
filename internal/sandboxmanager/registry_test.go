package sandboxmanager

import (
	"context"
	"encoding/json"
	"testing"

	"petris.dev/toby/internal/control"
)

type testService struct {
	commands []Command
}

func (s testService) Commands() []Command { return s.commands }

func TestRegistryRejectsDuplicateCommands(t *testing.T) {
	_, err := NewRegistry(RegistryParams{Services: []Service{
		testService{commands: []Command{CommandFunc{Name: "test.run", Run: okCommand}}},
		testService{commands: []Command{CommandFunc{Name: "test.run", Run: okCommand}}},
	}})
	if err == nil {
		t.Fatal("expected duplicate command to fail")
	}
}

func TestRegistryDispatchesRegisteredCommand(t *testing.T) {
	called := false
	registry, err := NewRegistry(RegistryParams{Services: []Service{testService{commands: []Command{CommandFunc{
		Name: "test.run",
		Run: func(ctx context.Context, runtime *Runtime, req control.RPCRequest) ([]byte, error) {
			called = true
			return control.ResponseOK(req.ID, control.EmptyResult{}), nil
		},
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := registry.Handle(context.Background(), nil, testRequest("test.run"))
	if err != nil {
		t.Fatal(err)
	}
	mustOK(t, response)
	if !called {
		t.Fatal("registered command was not called")
	}
}

func TestRegistryReturnsMethodNotFound(t *testing.T) {
	registry, err := NewRegistry(RegistryParams{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := registry.Handle(context.Background(), nil, testRequest("missing.run"))
	if err == nil {
		t.Fatal("expected method-not-found error")
	}
	decoded, err := control.DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != control.CodeMethodNotFound {
		t.Fatalf("response = %#v, want method-not-found", decoded)
	}
}

func okCommand(_ context.Context, _ *Runtime, req control.RPCRequest) ([]byte, error) {
	return control.ResponseOK(req.ID, control.EmptyResult{}), nil
}

func testRequest(method string) control.RPCRequest {
	return control.RPCRequest{JSONRPC: control.JSONRPCVersion, ID: json.RawMessage("1"), Method: method}
}
