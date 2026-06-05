package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubCapability struct {
	methods []Method
}

func (c stubCapability) Methods() []Method { return c.methods }

func okMethod(name string) Method {
	return Method{Name: name, Handle: func(_ context.Context, req RPCRequest) ([]byte, error) {
		return ResponseOK(req.ID, EmptyResult{}), nil
	}}
}

func TestNewRouterRejectsInvalidHandlers(t *testing.T) {
	if _, err := NewRouter([]Capability{stubCapability{methods: []Method{{Name: "", Handle: func(context.Context, RPCRequest) ([]byte, error) { return nil, nil }}}}}); err == nil {
		t.Fatal("expected empty method name to fail")
	}
	if _, err := NewRouter([]Capability{stubCapability{methods: []Method{{Name: "x.y"}}}}); err == nil {
		t.Fatal("expected nil handler to fail")
	}
	if _, err := NewRouter([]Capability{
		stubCapability{methods: []Method{okMethod("dup.run")}},
		stubCapability{methods: []Method{okMethod("dup.run")}},
	}); err == nil {
		t.Fatal("expected duplicate method to fail")
	}
}

func TestRouterDispatchesAndReportsUnknownMethod(t *testing.T) {
	called := false
	router, err := NewRouter([]Capability{
		nil, // nil capabilities are skipped
		stubCapability{methods: []Method{{Name: "do.it", Handle: func(_ context.Context, req RPCRequest) ([]byte, error) {
			called = true
			return ResponseOK(req.ID, EmptyResult{}), nil
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := RPCRequest{JSONRPC: JSONRPCVersion, ID: json.RawMessage("1"), Method: "do.it"}
	if _, err := router.Handle(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler was not invoked")
	}

	missing := RPCRequest{JSONRPC: JSONRPCVersion, ID: json.RawMessage("2"), Method: "nope"}
	response, err := router.Handle(context.Background(), missing)
	if !errors.Is(err, ErrMethodNotFound) {
		t.Fatalf("err = %v, want ErrMethodNotFound", err)
	}
	decoded, err := DecodeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Code != CodeMethodNotFound {
		t.Fatalf("response = %#v, want method-not-found", decoded)
	}
}
