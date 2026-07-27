package httpbridge

// Exercises protocol-transparent relay behavior and per-connection sessions.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const testProtocolVersion = "2025-06-18"

type protocolPeer struct {
	net.Conn
	decoder *json.Decoder
}

func newProtocolPeer(connection net.Conn) *protocolPeer {
	return &protocolPeer{
		Conn:    connection,
		decoder: json.NewDecoder(connection),
	}
}

func (p *protocolPeer) write(t *testing.T, message jsonrpc.Message) {
	t.Helper()

	data, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	data = append(data, '\n')

	if err := p.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if _, err := p.Write(data); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

func (p *protocolPeer) read(t *testing.T) jsonrpc.Message {
	t.Helper()

	if err := p.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	var raw json.RawMessage
	if err := p.decoder.Decode(&raw); err != nil {
		t.Fatalf("read message: %v", err)
	}

	message, err := jsonrpc.DecodeMessage(raw)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return message
}

func call(t *testing.T, id, method string, params any) *jsonrpc.Request {
	t.Helper()

	requestID, err := jsonrpc.MakeID(id)
	if err != nil {
		t.Errorf("construct request ID: %v", err)
		return &jsonrpc.Request{}
	}
	return &jsonrpc.Request{
		ID:     requestID,
		Method: method,
		Params: marshalRaw(t, params),
	}
}

func notification(t *testing.T, method string, params any) *jsonrpc.Request {
	t.Helper()

	return &jsonrpc.Request{
		Method: method,
		Params: marshalRaw(t, params),
	}
}

func response(t *testing.T, id jsonrpc.ID, result any) *jsonrpc.Response {
	t.Helper()

	return &jsonrpc.Response{
		ID:     id,
		Result: marshalRaw(t, result),
	}
}

func marshalRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Errorf("marshal JSON-RPC value: %v", err)
		return nil
	}
	return data
}

func initialize(t *testing.T, peer *protocolPeer, id string) {
	t.Helper()

	peer.write(t, call(t, id, methodInitialize, map[string]any{
		"protocolVersion": testProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "bridge-test",
			"version": "1",
		},
	}))

	message := peer.read(t)
	received, ok := message.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("initialize response type = %T, want *jsonrpc.Response", message)
	}
	if received.ID.Raw() != id {
		t.Fatalf("initialize response ID = %v, want %q", received.ID.Raw(), id)
	}

	peer.write(t, notification(t, methodInitialized, map[string]any{}))
}

func writeHTTPMessage(t *testing.T, writer http.ResponseWriter, message jsonrpc.Message) {
	t.Helper()

	data, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		t.Errorf("encode HTTP response: %v", err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write(data); err != nil {
		t.Errorf("write HTTP response: %v", err)
	}
}

func writeHTTPEvent(t *testing.T, writer http.ResponseWriter, message jsonrpc.Message) {
	t.Helper()

	data, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		t.Errorf("encode HTTP event: %v", err)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	if _, err := fmt.Fprintf(writer, "event: message\ndata: %s\n\n", data); err != nil {
		t.Errorf("write HTTP event: %v", err)
	}
}

func readHTTPRequest(t *testing.T, request *http.Request) jsonrpc.Message {
	t.Helper()

	data, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read HTTP request: %v", err)
		return nil
	}

	message, err := jsonrpc.DecodeMessage(data)
	if err != nil {
		t.Errorf("decode HTTP request: %v", err)
		return nil
	}
	return message
}

func TestBridgeForwardsUnknownMessagesAndSessionHeaders(t *testing.T) {
	const (
		sessionID = "session-transparent"
		secret    = "Bearer bridge-secret"
	)

	var (
		mu      sync.Mutex
		headers = make(map[string]http.Header)
	)
	deleted := make(chan http.Header, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			select {
			case deleted <- request.Header.Clone():
			default:
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		message := readHTTPRequest(t, request)
		rpcRequest, ok := message.(*jsonrpc.Request)
		if !ok {
			t.Errorf("HTTP message type = %T, want *jsonrpc.Request", message)
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		mu.Lock()
		headers[rpcRequest.Method] = request.Header.Clone()
		mu.Unlock()

		switch rpcRequest.Method {
		case methodInitialize:
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPEvent(t, writer, response(t, rpcRequest.ID, map[string]any{
				"protocolVersion": testProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "transparent-test",
					"version": "1",
				},
			}))
		case "experimental/arbitrary":
			writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
				"method": rpcRequest.Method,
				"raw":    json.RawMessage(rpcRequest.Params),
			}))
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	configuredHeaders := http.Header{
		"Authorization": {secret},
		"X-Tenant":      {"alpha"},
	}
	go func() {
		serveDone <- bridge.Serve(t.Context(), host, Upstream{
			Endpoint: server.URL,
			Headers:  configuredHeaders,
		})
	}()

	initialize(t, peer, "init-transparent")

	peer.write(t, call(t, "opaque-string-id", "experimental/arbitrary", map[string]any{
		"unknown": []any{1.0, "two", true},
	}))
	message := peer.read(t)
	customResponse, ok := message.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("custom response type = %T, want *jsonrpc.Response", message)
	}
	if customResponse.ID.Raw() != "opaque-string-id" {
		t.Fatalf("custom response ID = %v, want opaque-string-id", customResponse.ID.Raw())
	}

	var result struct {
		Method string          `json:"method"`
		Raw    json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(customResponse.Result, &result); err != nil {
		t.Fatalf("decode custom result: %v", err)
	}
	if result.Method != "experimental/arbitrary" {
		t.Fatalf("custom method = %q, want experimental/arbitrary", result.Method)
	}
	if !strings.Contains(string(result.Raw), `"unknown"`) {
		t.Fatalf("custom params = %s, want unknown field", result.Raw)
	}

	peer.write(t, notification(t, "notifications/experimental_changed", map[string]any{
		"opaque": "value",
	}))

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	var deleteHeaders http.Header
	select {
	case deleteHeaders = <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session was not deleted")
	}
	if deleteHeaders.Get("Authorization") != secret {
		t.Errorf(
			"DELETE Authorization = %q, want configured value",
			deleteHeaders.Get("Authorization"),
		)
	}
	if deleteHeaders.Get(sessionIDHeader) != sessionID {
		t.Errorf(
			"DELETE session ID = %q, want %q",
			deleteHeaders.Get(sessionIDHeader),
			sessionID,
		)
	}
	if deleteHeaders.Get(protocolVersionHeader) != testProtocolVersion {
		t.Errorf(
			"DELETE protocol version = %q, want %q",
			deleteHeaders.Get(protocolVersionHeader),
			testProtocolVersion,
		)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, method := range []string{methodInitialize, methodInitialized, "experimental/arbitrary"} {
		got := headers[method]
		if got.Get("Authorization") != secret {
			t.Errorf("%s Authorization = %q, want configured value", method, got.Get("Authorization"))
		}
		if got.Get("X-Tenant") != "alpha" {
			t.Errorf("%s X-Tenant = %q, want alpha", method, got.Get("X-Tenant"))
		}
	}
	for _, method := range []string{methodInitialized, "experimental/arbitrary"} {
		got := headers[method]
		if got.Get(sessionIDHeader) != sessionID {
			t.Errorf("%s session ID = %q, want %q", method, got.Get(sessionIDHeader), sessionID)
		}
		if got.Get(protocolVersionHeader) != testProtocolVersion {
			t.Errorf("%s protocol version = %q, want %q", method, got.Get(protocolVersionHeader), testProtocolVersion)
		}
	}
	if configuredHeaders.Get("Authorization") != secret {
		t.Fatalf("Serve mutated configured headers")
	}
}

func TestBridgeUsesIndependentSessionsWithSharedClient(t *testing.T) {
	var (
		nextSession atomic.Int64
		deletes     atomic.Int64
	)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			deletes.Add(1)
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		message := readHTTPRequest(t, request)
		rpcRequest, ok := message.(*jsonrpc.Request)
		if !ok {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		switch rpcRequest.Method {
		case methodInitialize:
			sessionID := "session-" + strconv.FormatInt(nextSession.Add(1), 10)
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
				"protocolVersion": testProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "sessions", "version": "1"},
			}))
		case "session/id":
			writeHTTPMessage(t, writer, response(t, rpcRequest.ID, request.Header.Get(sessionIDHeader)))
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	sharedClient := server.Client()
	bridge, err := New(Options{HTTPClient: sharedClient})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type runningSession struct {
		peer *protocolPeer
		done <-chan error
	}
	start := func() runningSession {
		host, sandbox := net.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- bridge.Serve(t.Context(), host, Upstream{Endpoint: server.URL})
		}()
		return runningSession{
			peer: newProtocolPeer(sandbox),
			done: done,
		}
	}

	first := start()
	second := start()
	initialize(t, first.peer, "init-first")
	initialize(t, second.peer, "init-second")

	sessionID := func(session runningSession, id string) string {
		session.peer.write(t, call(t, id, "session/id", map[string]any{}))
		message := session.peer.read(t)
		rpcResponse, ok := message.(*jsonrpc.Response)
		if !ok {
			t.Fatalf("session response type = %T, want *jsonrpc.Response", message)
		}

		var value string
		if err := json.Unmarshal(rpcResponse.Result, &value); err != nil {
			t.Fatalf("decode session ID: %v", err)
		}
		return value
	}

	firstID := sessionID(first, "first-id")
	secondID := sessionID(second, "second-id")
	if firstID == secondID {
		t.Fatalf("two bridge connections shared MCP session %q", firstID)
	}

	if err := first.peer.Close(); err != nil {
		t.Fatalf("close first peer: %v", err)
	}
	if err := second.peer.Close(); err != nil {
		t.Fatalf("close second peer: %v", err)
	}
	if err := <-first.done; err != nil {
		t.Fatalf("first Serve: %v", err)
	}
	if err := <-second.done; err != nil {
		t.Fatalf("second Serve: %v", err)
	}
	if got := deletes.Load(); got != 2 {
		t.Fatalf("DELETE requests = %d, want 2", got)
	}
}

func TestBridgeCancellationClosesUpstreamSession(t *testing.T) {
	deleted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			deleted <- struct{}{}
			writer.WriteHeader(http.StatusNoContent)
		default:
			message := readHTTPRequest(t, request)
			rpcRequest, ok := message.(*jsonrpc.Request)
			if ok && rpcRequest.Method == methodInitialize {
				writer.Header().Set(sessionIDHeader, "session-cancel")
				writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "cancel", "version": "1"},
				}))
				return
			}
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	host, sandbox := net.Pipe()
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(ctx, host, Upstream{Endpoint: server.URL})
	}()

	initialize(t, peer, "init-cancel")
	cancel()

	if err := <-serveDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve error = %v, want context.Canceled", err)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not delete upstream session")
	}

	_ = peer.Close()
}

func TestBridgeForwardsCancellationWhileCallIsPending(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	cancelReceived := make(chan struct{}, 1)
	releaseSlow := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		message := readHTTPRequest(t, request)
		rpcRequest, ok := message.(*jsonrpc.Request)
		if !ok {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		switch rpcRequest.Method {
		case methodInitialize:
			writer.Header().Set(sessionIDHeader, "session-concurrent")
			writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
				"protocolVersion": testProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "concurrent", "version": "1"},
			}))
		case "custom/slow":
			slowStarted <- struct{}{}
			select {
			case <-releaseSlow:
				writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
					"cancelObserved": true,
				}))
			case <-request.Context().Done():
				return
			}
		case "notifications/cancelled":
			cancelReceived <- struct{}{}
			releaseSlow <- struct{}{}
			writer.WriteHeader(http.StatusAccepted)
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(t.Context(), host, Upstream{Endpoint: server.URL})
	}()

	initialize(t, peer, "init-concurrent")
	peer.write(t, call(t, "slow-call", "custom/slow", map[string]any{}))

	select {
	case <-slowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("slow upstream call did not start")
	}

	peer.write(t, notification(t, "notifications/cancelled", map[string]any{
		"requestId": "slow-call",
		"reason":    "test cancellation",
	}))

	select {
	case <-cancelReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation was blocked behind the pending HTTP call")
	}

	message := peer.read(t)
	slowResponse, ok := message.(*jsonrpc.Response)
	if !ok || slowResponse.ID.Raw() != "slow-call" {
		t.Fatalf("slow response = %#v, want response for slow-call", message)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestBridgeDisconnectCancelsPendingHTTPCall(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	slowCanceled := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
			return
		}

		message := readHTTPRequest(t, request)
		rpcRequest, ok := message.(*jsonrpc.Request)
		if !ok {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		switch rpcRequest.Method {
		case methodInitialize:
			writer.Header().Set(sessionIDHeader, "session-disconnect")
			writeHTTPMessage(t, writer, response(t, rpcRequest.ID, map[string]any{
				"protocolVersion": testProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "disconnect", "version": "1"},
			}))
		case "custom/blocked":
			slowStarted <- struct{}{}
			<-request.Context().Done()
			slowCanceled <- struct{}{}
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := net.Pipe()
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(t.Context(), host, Upstream{Endpoint: server.URL})
	}()

	initialize(t, peer, "init-disconnect")
	peer.write(t, call(t, "blocked-call", "custom/blocked", map[string]any{}))

	select {
	case <-slowStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked upstream call did not start")
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	select {
	case err := <-serveDone:
		if err == nil ||
			!strings.Contains(err.Error(), "drain accepted MCP HTTP writes") {
			t.Fatalf(
				"Serve error = %v, want accepted-write drain timeout",
				err,
			)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after downstream disconnected")
	}
	select {
	case <-slowCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("pending HTTP call was not canceled")
	}
}
