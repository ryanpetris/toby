package hostaction

// JSON-RPC 2.0 envelope: the request/response/error types, the standard error
// codes, and helpers to build and parse the framing. Capability packages such
// as internal/hostaction/methods/git own their method names, typed parameters,
// results, builders, and decoders.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// JSONRPCVersion is the supported JSON-RPC protocol version.
const JSONRPCVersion = "2.0"

const (
	// CodeInvalidRequest reports an invalid JSON-RPC request.
	CodeInvalidRequest = -32600
	// CodeMethodNotFound reports an unknown RPC method.
	CodeMethodNotFound = -32601
	// CodeInvalidParams reports invalid method parameters.
	CodeInvalidParams = -32602
	// CodeInternalError reports an internal handler failure.
	CodeInternalError = -32603
	// CodeProjectNotVisible reports an inaccessible repository capability.
	CodeProjectNotVisible = -32007
	// CodePermissionDenied reports a denied host action.
	CodePermissionDenied = -32008
)

// RPCRequest is one JSON-RPC request.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse is one JSON-RPC result or error.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error returns the human-readable failure message.
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// NewRequest builds a JSON-RPC request envelope with an integer ID. Typed
// capability builders wrap this with their method names and marshaled params.
func NewRequest(id int64, method string, params json.RawMessage) ([]byte, error) {
	idBytes, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(RPCRequest{JSONRPC: JSONRPCVersion, ID: idBytes, Method: method, Params: params})
}

// DecodeRequest decodes and validates a JSON-RPC request.
func DecodeRequest(data []byte) (RPCRequest, error) {
	var req RPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return RPCRequest{}, fmt.Errorf("parse request: %w", err)
	}
	if req.JSONRPC != JSONRPCVersion || len(req.ID) == 0 || req.Method == "" {
		return RPCRequest{}, errors.New("invalid JSON-RPC request")
	}
	return req, nil
}

// ResponseOK encodes a successful JSON-RPC response.
func ResponseOK(id json.RawMessage, result any) []byte {
	data, err := json.Marshal(RPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      cloneID(id),
		Result:  result,
	})
	if err != nil {
		return ResponseError(
			id,
			CodeInternalError,
			"encode host-action response",
			nil,
		)
	}
	return append(data, '\n')
}

// ResponseError encodes a failed JSON-RPC response.
func ResponseError(id json.RawMessage, code int, message string, data any) []byte {
	resp := RPCResponse{JSONRPC: JSONRPCVersion, ID: cloneID(id), Error: &RPCError{Code: code, Message: message, Data: data}}
	encoded, err := json.Marshal(resp)
	if err != nil {
		encoded, err = json.Marshal(RPCResponse{
			JSONRPC: JSONRPCVersion,
			ID:      cloneID(id),
			Error: &RPCError{
				Code:    CodeInternalError,
				Message: "encode host-action error response",
			},
		})
		if err != nil {
			return []byte(
				"{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32603,\"message\":\"encode host-action error response\"}}\n",
			)
		}
	}
	return append(encoded, '\n')
}

// DecodeResponse decodes and validates a JSON-RPC response.
func DecodeResponse(data []byte) (RPCResponse, error) {
	var resp RPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return RPCResponse{}, err
	}
	if resp.JSONRPC != JSONRPCVersion {
		return RPCResponse{}, errors.New("invalid JSON-RPC response")
	}
	return resp, nil
}

func cloneID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return append(json.RawMessage(nil), id...)
}
