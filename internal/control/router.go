package control

// Method dispatch: a Handler (a capability) contributes one or more named Methods;
// a Router collects the handlers registered for one side of the channel and
// dispatches an incoming request to the matching method. Capabilities hold their
// own dependencies, so a Method's handler takes only the request.

import (
	"context"
	"errors"
	"fmt"
)

// ErrMethodNotFound is returned (alongside a JSON-RPC error response) when no
// handler is registered for a request's method.
var ErrMethodNotFound = errors.New("control: method not found")

// MethodHandler processes one request and returns the response bytes.
type MethodHandler func(context.Context, RPCRequest) ([]byte, error)

// Method binds a JSON-RPC method name to its handler.
type Method struct {
	Name   string
	Handle MethodHandler
}

// Capability contributes a set of methods to a Router. Concrete capability
// services implement it and are collected via an fx group.
type Capability interface {
	Methods() []Method
}

// Router dispatches requests to the method registered for them.
type Router struct {
	methods map[string]MethodHandler
}

// NewRouter indexes the methods contributed by the given handlers, rejecting
// duplicates and malformed entries.
func NewRouter(handlers []Capability) (*Router, error) {
	router := &Router{methods: map[string]MethodHandler{}}
	for _, capability := range handlers {
		if capability == nil {
			continue
		}
		for _, method := range capability.Methods() {
			if method.Name == "" {
				return nil, fmt.Errorf("control: handler method must define a name")
			}
			if method.Handle == nil {
				return nil, fmt.Errorf("control: method %q has no handler", method.Name)
			}
			if _, exists := router.methods[method.Name]; exists {
				return nil, fmt.Errorf("control: duplicate method %q", method.Name)
			}
			router.methods[method.Name] = method.Handle
		}
	}

	return router, nil
}

// Handle dispatches req to its method, returning a JSON-RPC error response when no
// method matches.
func (r *Router) Handle(ctx context.Context, req RPCRequest) ([]byte, error) {
	if r == nil {
		return ResponseError(req.ID, CodeInternalError, "control router is not configured", nil), ErrMethodNotFound
	}
	handle, ok := r.methods[req.Method]
	if !ok {
		return ResponseError(req.ID, CodeMethodNotFound, "method not found: "+req.Method, nil), ErrMethodNotFound
	}

	return handle(ctx, req)
}
