package control

// JSON-RPC 2.0 envelope: the request/response/error types, the standard error
// codes, and helpers to build and parse the framing. The Toby-specific method
// contract (method names, typed params/results, builders, decoders) lives in
// control/protocol.

import (
	"encoding/json"
	"errors"
	"fmt"
)

const JSONRPCVersion = "2.0"

const (
	CodeParseError        = -32700
	CodeInvalidRequest    = -32600
	CodeMethodNotFound    = -32601
	CodeInvalidParams     = -32602
	CodeInternalError     = -32603
	CodeProjectNotVisible = -32007
	CodePermissionDenied  = -32008
)

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// NewRequest builds a JSON-RPC request envelope with an integer id. Typed builders
// in control/protocol wrap this with their method names and marshaled params.
func NewRequest(id int64, method string, params json.RawMessage) ([]byte, error) {
	idBytes, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(RPCRequest{JSONRPC: JSONRPCVersion, ID: idBytes, Method: method, Params: params})
}

// NewNotification builds a JSON-RPC notification (a method call with no id, expecting
// no response) — used for one-way streaming such as exec output.
func NewNotification(method string, params json.RawMessage) ([]byte, error) {
	return json.Marshal(RPCRequest{JSONRPC: JSONRPCVersion, Method: method, Params: params})
}

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

func ResponseOK(id json.RawMessage, result any) []byte {
	data, _ := json.Marshal(RPCResponse{JSONRPC: JSONRPCVersion, ID: cloneID(id), Result: result})
	return append(data, '\n')
}

func ResponseError(id json.RawMessage, code int, message string, data any) []byte {
	resp := RPCResponse{JSONRPC: JSONRPCVersion, ID: cloneID(id), Error: &RPCError{Code: code, Message: message, Data: data}}
	encoded, _ := json.Marshal(resp)
	return append(encoded, '\n')
}

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
