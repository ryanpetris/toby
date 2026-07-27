package httpbridge

// Exercises server-initiated requests carried by the standalone SSE stream.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func TestScanEventMessagesPreservesCustomJSONRPC(t *testing.T) {
	input := strings.NewReader(
		": keepalive\n" +
			"event: ignored\n" +
			"data: {\"not\":\"json-rpc\"}\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":\"custom-id\",\"method\":\"custom/server\",\"params\":{\"x\":1}}\n\n",
	)

	var messages []jsonrpc.Message
	cursor := eventCursor{retry: defaultEventRetry}
	err := scanEventMessages(
		input,
		defaultMaxMessageSize,
		&cursor,
		func(message jsonrpc.Message) error {
			messages = append(messages, message)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("scanEventMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	request, ok := messages[0].(*jsonrpc.Request)
	if !ok || request.Method != "custom/server" || request.ID.Raw() != "custom-id" {
		t.Fatalf("message = %#v, want custom/server call", messages[0])
	}
}

func TestScanEventMessagesTracksResumeCursor(t *testing.T) {
	input := strings.NewReader(
		"id: first-event\n" +
			"retry: 25\n" +
			"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/one\"}\n\n" +
			"id: second-event\n" +
			"retry: 999999999999999999\n" +
			"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/two\"}\n\n",
	)

	cursor := eventCursor{retry: defaultEventRetry}
	var messages int
	err := scanEventMessages(
		input,
		defaultMaxMessageSize,
		&cursor,
		func(jsonrpc.Message) error {
			messages++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("scanEventMessages: %v", err)
	}
	if messages != 2 {
		t.Fatalf("messages = %d, want 2", messages)
	}
	if cursor.lastEventID != "second-event" {
		t.Fatalf(
			"last event ID = %q, want second-event",
			cursor.lastEventID,
		)
	}
	if cursor.retry != maximumEventRetry {
		t.Fatalf(
			"retry delay = %v, want %v",
			cursor.retry,
			maximumEventRetry,
		)
	}
}

func TestBridgeResumesStandaloneEventStream(t *testing.T) {
	const sessionID = "session-resume"

	deleted := make(chan struct{}, 1)
	var getCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.Method {
		case http.MethodDelete:
			deleted <- struct{}{}
			writer.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet:
			count := getCount.Add(1)
			if request.Header.Get(sessionIDHeader) != sessionID {
				t.Errorf(
					"GET session ID = %q, want %q",
					request.Header.Get(sessionIDHeader),
					sessionID,
				)
			}
			if request.Header.Get(protocolVersionHeader) != testProtocolVersion {
				t.Errorf(
					"GET protocol version = %q, want %q",
					request.Header.Get(protocolVersionHeader),
					testProtocolVersion,
				)
			}

			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)

			var message jsonrpc.Message
			switch count {
			case 1:
				if got := request.Header.Get("Last-Event-ID"); got != "" {
					t.Errorf("initial Last-Event-ID = %q, want empty", got)
				}
				if _, err := fmt.Fprint(
					writer,
					"event: prime\nid: event-one\nretry: 1\n\n",
				); err != nil {
					t.Errorf("write standalone prime event: %v", err)
				}
				return
			case 2:
				if got := request.Header.Get("Last-Event-ID"); got != "event-one" {
					t.Errorf(
						"resumed Last-Event-ID = %q, want event-one",
						got,
					)
				}
				message = call(
					t,
					"resumed-call",
					"sampling/createMessage",
					map[string]any{},
				)
			default:
				t.Errorf("standalone GET requests = %d, want at most 2", count)
				return
			}

			data, err := jsonrpc.EncodeMessage(message)
			if err != nil {
				t.Errorf("encode standalone event: %v", err)
				return
			}
			if _, err := fmt.Fprintf(
				writer,
				"id: %s\nretry: 1\ndata: %s\n\n",
				"event-two",
				data,
			); err != nil {
				t.Errorf("write standalone event: %v", err)
				return
			}
			flusher, ok := writer.(http.Flusher)
			if !ok {
				t.Error("test HTTP writer does not support flushing")
				return
			}
			flusher.Flush()

			if count == 2 {
				<-request.Context().Done()
			}
			return
		}

		message := readHTTPRequest(t, request)
		switch rpcMessage := message.(type) {
		case *jsonrpc.Request:
			if rpcMessage.Method == methodInitialize {
				writer.Header().Set(sessionIDHeader, sessionID)
				writeHTTPMessage(t, writer, response(
					t,
					rpcMessage.ID,
					map[string]any{
						"protocolVersion": testProtocolVersion,
						"capabilities":    map[string]any{},
						"serverInfo": map[string]any{
							"name":    "resume",
							"version": "1",
						},
					},
				))
				return
			}
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
		serveDone <- bridge.Serve(
			t.Context(),
			host,
			Upstream{Endpoint: server.URL},
		)
	}()

	initialize(t, peer, "init-resume")

	message := peer.read(t)
	resumedCall, ok := message.(*jsonrpc.Request)
	if !ok ||
		resumedCall.Method != "sampling/createMessage" ||
		resumedCall.ID.Raw() != "resumed-call" {
		t.Fatalf("resumed-stream message = %#v, want resumed call", message)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session was not deleted")
	}
	if got := getCount.Load(); got != 2 {
		t.Fatalf("standalone GET requests = %d, want 2", got)
	}
}

func TestBridgeRelaysStandaloneServerRequestAndResponse(t *testing.T) {
	const sessionID = "session-standalone"

	serverResponse := make(chan *jsonrpc.Response, 1)
	responseReceived := make(chan struct{}, 1)
	streamOpened := make(chan struct{}, 1)
	deleted := make(chan struct{}, 1)
	var getCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodDelete:
			deleted <- struct{}{}
			writer.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet:
			getCount.Add(1)
			if request.Header.Get(sessionIDHeader) != sessionID {
				t.Errorf("GET session ID = %q, want %q", request.Header.Get(sessionIDHeader), sessionID)
			}
			if request.Header.Get(protocolVersionHeader) != testProtocolVersion {
				t.Errorf(
					"GET protocol version = %q, want %q",
					request.Header.Get(protocolVersionHeader),
					testProtocolVersion,
				)
			}

			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			flusher, ok := writer.(http.Flusher)
			if !ok {
				t.Error("test HTTP writer does not support flushing")
				return
			}
			streamOpened <- struct{}{}

			serverCall := call(t, "server-call", "sampling/createMessage", map[string]any{
				"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			})
			data, err := jsonrpc.EncodeMessage(serverCall)
			if err != nil {
				t.Errorf("encode server call: %v", err)
				return
			}
			if _, err := fmt.Fprintf(writer, "event: message\ndata: %s\n\n", data); err != nil {
				t.Errorf("write server call event: %v", err)
				return
			}
			flusher.Flush()

			select {
			case <-responseReceived:
				changed := notification(t, "notifications/tools/list_changed", map[string]any{
					"reason": "response-observed",
				})
				data, err := jsonrpc.EncodeMessage(changed)
				if err != nil {
					t.Errorf("encode server notification: %v", err)
					return
				}
				if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
					t.Errorf("write server notification event: %v", err)
					return
				}
				flusher.Flush()
			case <-request.Context().Done():
				return
			}

			<-request.Context().Done()
			return
		}

		message := readHTTPRequest(t, request)
		switch rpcMessage := message.(type) {
		case *jsonrpc.Request:
			if rpcMessage.Method == methodInitialize {
				writer.Header().Set(sessionIDHeader, sessionID)
				writeHTTPMessage(t, writer, response(t, rpcMessage.ID, map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "standalone", "version": "1"},
				}))
				return
			}
			writer.WriteHeader(http.StatusAccepted)
		case *jsonrpc.Response:
			if request.Header.Get(sessionIDHeader) != sessionID {
				t.Errorf(
					"response POST session ID = %q, want %q",
					request.Header.Get(sessionIDHeader),
					sessionID,
				)
			}
			if request.Header.Get(protocolVersionHeader) != testProtocolVersion {
				t.Errorf(
					"response POST protocol version = %q, want %q",
					request.Header.Get(protocolVersionHeader),
					testProtocolVersion,
				)
			}
			serverResponse <- rpcMessage
			responseReceived <- struct{}{}
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
	t.Cleanup(func() {
		_ = peer.Close()
	})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(t.Context(), host, Upstream{Endpoint: server.URL})
	}()

	initialize(t, peer, "init-standalone")
	select {
	case <-streamOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone stream did not open")
	}

	message := peer.read(t)
	serverCall, ok := message.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("standalone message type = %T, want *jsonrpc.Request", message)
	}
	if serverCall.Method != "sampling/createMessage" {
		t.Fatalf("standalone method = %q, want sampling/createMessage", serverCall.Method)
	}
	if serverCall.ID.Raw() != "server-call" {
		t.Fatalf("standalone ID = %v, want server-call", serverCall.ID.Raw())
	}

	peer.write(t, response(t, serverCall.ID, map[string]any{
		"model": "test-model",
		"content": map[string]any{
			"type": "text",
			"text": "world",
		},
	}))

	notificationMessage := peer.read(t)
	changed, ok := notificationMessage.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("standalone notification type = %T, want *jsonrpc.Request", notificationMessage)
	}
	if changed.Method != "notifications/tools/list_changed" || changed.ID.IsValid() {
		t.Fatalf(
			"standalone notification = method %q, ID %v",
			changed.Method,
			changed.ID.Raw(),
		)
	}

	select {
	case got := <-serverResponse:
		if got.ID.Raw() != "server-call" {
			t.Fatalf("server response ID = %v, want server-call", got.ID.Raw())
		}
		var result map[string]any
		if err := json.Unmarshal(got.Result, &result); err != nil {
			t.Fatalf("decode server response: %v", err)
		}
		if result["model"] != "test-model" {
			t.Fatalf("server response model = %v, want test-model", result["model"])
		}
	default:
		t.Fatal("server did not receive downstream response")
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session was not deleted")
	}
	if got := getCount.Load(); got != 1 {
		t.Fatalf("standalone GET requests = %d, want 1", got)
	}
}

func TestBridgeOpensStandaloneStreamWithoutSessionID(t *testing.T) {
	streamOpened := make(chan struct{}, 1)
	var deleteCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.Method {
		case http.MethodDelete:
			deleteCount.Add(1)
			writer.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet:
			if _, present := request.Header[http.CanonicalHeaderKey(
				sessionIDHeader,
			)]; present {
				t.Errorf("sessionless GET included %s", sessionIDHeader)
			}
			if request.Header.Get(protocolVersionHeader) !=
				testProtocolVersion {
				t.Errorf(
					"GET protocol version = %q, want %q",
					request.Header.Get(protocolVersionHeader),
					testProtocolVersion,
				)
			}

			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			flusher, ok := writer.(http.Flusher)
			if !ok {
				t.Error("test HTTP writer does not support flushing")
				return
			}

			changed := notification(
				t,
				"notifications/tools/list_changed",
				map[string]any{},
			)
			data, err := jsonrpc.EncodeMessage(changed)
			if err != nil {
				t.Errorf("encode server notification: %v", err)
				return
			}
			if _, err := fmt.Fprintf(
				writer,
				"event: message\ndata: %s\n\n",
				data,
			); err != nil {
				t.Errorf("write server notification event: %v", err)
				return
			}
			flusher.Flush()
			streamOpened <- struct{}{}

			<-request.Context().Done()
			return
		}

		message := readHTTPRequest(t, request)
		if rpcRequest, ok := message.(*jsonrpc.Request); ok &&
			rpcRequest.Method == methodInitialize {
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "sessionless",
						"version": "1",
					},
				},
			))
			return
		}
		writer.WriteHeader(http.StatusAccepted)
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
		serveDone <- bridge.Serve(
			t.Context(),
			host,
			Upstream{Endpoint: server.URL},
		)
	}()

	initialize(t, peer, "init-sessionless")
	select {
	case <-streamOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("sessionless standalone stream did not open")
	}

	message := peer.read(t)
	changed, ok := message.(*jsonrpc.Request)
	if !ok ||
		changed.Method != "notifications/tools/list_changed" ||
		changed.ID.IsValid() {
		t.Fatalf(
			"sessionless standalone message = %#v, want list-changed notification",
			message,
		)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got := deleteCount.Load(); got != 0 {
		t.Fatalf("sessionless DELETE requests = %d, want 0", got)
	}
}
