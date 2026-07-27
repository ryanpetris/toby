package httpbridge

// Exercises ordered dispatch, graceful half-close, and outstanding-call bounds.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return f(request)
}

func TestBridgeDrainsAcceptedWriteAfterDownstreamHalfClose(t *testing.T) {
	const sessionID = "session-half-close"

	finalStarted := make(chan struct{}, 1)
	finalAccepted := make(chan struct{}, 1)
	finalCanceled := make(chan struct{}, 1)
	releaseFinal := make(chan struct{})
	deleted := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		case http.MethodDelete:
			deleted <- struct{}{}
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
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "half-close",
						"version": "1",
					},
				},
			))
		case "notifications/final":
			finalStarted <- struct{}{}
			select {
			case <-releaseFinal:
				finalAccepted <- struct{}{}
				writer.WriteHeader(http.StatusAccepted)
			case <-request.Context().Done():
				finalCanceled <- struct{}{}
			}
		default:
			writer.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	bridge, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	host, sandbox := tcpConnectionPair(t)
	peer := newProtocolPeer(sandbox)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- bridge.Serve(
			t.Context(),
			host,
			Upstream{Endpoint: server.URL},
		)
	}()

	initialize(t, peer, "init-half-close")
	peer.write(t, notification(
		t,
		"notifications/final",
		map[string]any{"value": "complete"},
	))
	if err := sandbox.CloseWrite(); err != nil {
		t.Fatalf("half-close downstream writes: %v", err)
	}

	select {
	case <-finalStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("final upstream write did not start")
	}
	select {
	case <-finalCanceled:
		t.Fatal("final upstream write was canceled at downstream EOF")
	default:
	}
	close(releaseFinal)

	select {
	case <-finalAccepted:
	case <-time.After(5 * time.Second):
		t.Fatal("final upstream write was not accepted")
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session was not deleted")
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close downstream peer: %v", err)
	}
}

func TestBridgeBoundsOutstandingCallBodies(t *testing.T) {
	const sessionID = "session-call-limit"

	var (
		active     atomic.Int64
		maxActive  atomic.Int64
		started    atomic.Int64
		cancelOnce sync.Once
	)
	allStarted := make(chan struct{})
	allCanceled := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
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
		if rpcRequest.Method == methodInitialize {
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "call-limit",
						"version": "1",
					},
				},
			))
			return
		}
		if rpcRequest.Method == methodInitialized {
			writer.WriteHeader(http.StatusAccepted)
			return
		}

		current := active.Add(1)
		if started.Add(1) == maxOutstandingCalls {
			close(allStarted)
		}
		for {
			observed := maxActive.Load()
			if current <= observed ||
				maxActive.CompareAndSwap(observed, current) {
				break
			}
		}
		defer func() {
			if active.Add(-1) == 0 {
				cancelOnce.Do(func() {
					close(allCanceled)
				})
			}
		}()

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("test HTTP writer does not support flushing")
			return
		}
		flusher.Flush()
		<-request.Context().Done()
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

	initialize(t, peer, "init-call-limit")
	for index := range maxOutstandingCalls {
		peer.write(t, call(
			t,
			fmt.Sprintf("stalled-%d", index),
			"custom/stalled",
			map[string]any{},
		))
	}
	select {
	case <-allStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("outstanding calls did not reach the configured limit")
	}
	peer.write(t, call(
		t,
		"stalled-over-limit",
		"custom/stalled",
		map[string]any{},
	))

	select {
	case err := <-serveDone:
		if err == nil ||
			!strings.Contains(err.Error(), "outstanding call limit") {
			t.Fatalf(
				"Serve error = %v, want outstanding-call limit",
				err,
			)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not enforce the outstanding-call limit")
	}
	select {
	case <-allCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("stalled upstream calls were not canceled")
	}

	if got := started.Load(); got != maxOutstandingCalls {
		t.Fatalf(
			"started HTTP calls = %d, want %d",
			got,
			maxOutstandingCalls,
		)
	}
	if got := maxActive.Load(); got > maxOutstandingCalls {
		t.Fatalf(
			"maximum active HTTP calls = %d, want at most %d",
			got,
			maxOutstandingCalls,
		)
	}
	_ = peer.Close()
}

func TestBridgeAllowsCancellationAtFullCallCapacity(t *testing.T) {
	const sessionID = "session-full-capacity"

	streamOpened := make(chan struct{}, 1)
	allCallsStarted := make(chan struct{})
	cancelSeen := make(chan struct{}, 1)
	releaseSeen := make(chan struct{}, 1)
	releaseCalls := make(chan struct{})
	deleted := make(chan struct{}, 1)
	var started atomic.Int64

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
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			flusher, ok := writer.(http.Flusher)
			if !ok {
				t.Error("test HTTP writer does not support flushing")
				return
			}
			flusher.Flush()
			streamOpened <- struct{}{}
			<-request.Context().Done()
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
			writer.Header().Set(sessionIDHeader, sessionID)
			writeHTTPMessage(t, writer, response(
				t,
				rpcRequest.ID,
				map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "full-capacity",
						"version": "1",
					},
				},
			))
		case methodInitialized:
			writer.WriteHeader(http.StatusAccepted)
		case "custom/stalled":
			data, err := jsonrpc.EncodeMessage(response(
				t,
				rpcRequest.ID,
				map[string]any{"released": true},
			))
			if err != nil {
				t.Errorf("encode stalled response: %v", err)
				return
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			flusher, ok := writer.(http.Flusher)
			if !ok {
				t.Error("test HTTP writer does not support flushing")
				return
			}
			flusher.Flush()
			if started.Add(1) == maxOutstandingCalls {
				close(allCallsStarted)
			}

			select {
			case <-releaseCalls:
				if _, err := writer.Write(data); err != nil {
					t.Errorf("write stalled response: %v", err)
				}
			case <-request.Context().Done():
			}
		case "notifications/cancelled":
			cancelSeen <- struct{}{}
			writer.WriteHeader(http.StatusAccepted)
		case "notifications/release":
			close(releaseCalls)
			releaseSeen <- struct{}{}
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

	initialize(t, peer, "init-full-capacity")
	select {
	case <-streamOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone event stream did not open")
	}

	for index := range maxOutstandingCalls {
		peer.write(t, call(
			t,
			fmt.Sprintf("capacity-%d", index),
			"custom/stalled",
			map[string]any{},
		))
	}
	select {
	case <-allCallsStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("calls did not fill the outstanding-call capacity")
	}

	peer.write(t, notification(
		t,
		"notifications/cancelled",
		map[string]any{"requestId": "capacity-0"},
	))
	select {
	case <-cancelSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not reach a full-capacity upstream")
	}

	peer.write(t, notification(
		t,
		"notifications/release",
		map[string]any{},
	))
	select {
	case <-releaseSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge stopped before a post-cancellation notification")
	}

	responses := make(map[string]struct{}, maxOutstandingCalls)
	for range maxOutstandingCalls {
		message := peer.read(t)
		rpcResponse, ok := message.(*jsonrpc.Response)
		if !ok {
			t.Fatalf("released message type = %T, want response", message)
		}
		id, ok := rpcResponse.ID.Raw().(string)
		if !ok || !strings.HasPrefix(id, "capacity-") {
			t.Fatalf("released response ID = %v", rpcResponse.ID.Raw())
		}
		if _, duplicate := responses[id]; duplicate {
			t.Fatalf("duplicate released response ID %q", id)
		}
		responses[id] = struct{}{}
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream session was not deleted")
	}
}

func TestBridgeAdmitsDownstreamWritesWithoutResponseSerialization(t *testing.T) {
	const endpoint = "http://ordered.invalid/mcp"

	var (
		mu      sync.Mutex
		methods []string
	)
	firstEntered := make(chan struct{}, 1)
	secondEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})

	transport := roundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		if request.Method == http.MethodGet {
			return emptyHTTPResponse(
				request,
				http.StatusMethodNotAllowed,
			), nil
		}
		if request.Method == http.MethodDelete {
			return emptyHTTPResponse(request, http.StatusNoContent), nil
		}

		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		message, err := jsonrpc.DecodeMessage(data)
		if err != nil {
			return nil, err
		}
		rpcRequest, ok := message.(*jsonrpc.Request)
		if !ok {
			return emptyHTTPResponse(request, http.StatusAccepted), nil
		}

		mu.Lock()
		methods = append(methods, rpcRequest.Method)
		mu.Unlock()

		trace := httptrace.ContextClientTrace(request.Context())
		if trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}

		switch rpcRequest.Method {
		case methodInitialize:
			return messageHTTPResponse(
				t,
				request,
				"session-ordered",
				response(t, rpcRequest.ID, map[string]any{
					"protocolVersion": testProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "ordered",
						"version": "1",
					},
				}),
			), nil
		case methodInitialized:
			return emptyHTTPResponse(request, http.StatusAccepted), nil
		case "notifications/order_first":
			firstEntered <- struct{}{}
			select {
			case <-releaseFirst:
				return emptyHTTPResponse(
					request,
					http.StatusAccepted,
				), nil
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		case "custom/order_second":
			secondEntered <- struct{}{}
			return messageHTTPResponse(
				t,
				request,
				"",
				response(t, rpcRequest.ID, map[string]any{
					"ordered": true,
				}),
			), nil
		default:
			return emptyHTTPResponse(request, http.StatusAccepted), nil
		}
	})

	bridge, err := New(Options{
		HTTPClient: &http.Client{Transport: transport},
	})
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
			Upstream{Endpoint: endpoint},
		)
	}()

	initialize(t, peer, "init-ordered")
	peer.write(t, notification(
		t,
		"notifications/order_first",
		map[string]any{},
	))
	peer.write(t, call(
		t,
		"order-second",
		"custom/order_second",
		map[string]any{},
	))

	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not enter the HTTP transport")
	}
	select {
	case <-secondEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second request was blocked behind first response headers")
	}
	close(releaseFirst)

	message := peer.read(t)
	rpcResponse, ok := message.(*jsonrpc.Response)
	if !ok || rpcResponse.ID.Raw() != "order-second" {
		t.Fatalf("ordered response = %#v", message)
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("close peer: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		methodInitialize,
		methodInitialized,
		"notifications/order_first",
		"custom/order_second",
	}
	if len(methods) != len(want) {
		t.Fatalf("HTTP dispatch methods = %v, want %v", methods, want)
	}
	for index := range want {
		if methods[index] != want[index] {
			t.Fatalf(
				"HTTP dispatch methods = %v, want %v",
				methods,
				want,
			)
		}
	}
}

func tcpConnectionPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for TCP pair: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()

	clientConnection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial TCP pair: %v", err)
	}
	select {
	case err := <-acceptErr:
		clientConnection.Close()
		t.Fatalf("accept TCP pair: %v", err)
	case serverConnection := <-accepted:
		client, clientOK := clientConnection.(*net.TCPConn)
		server, serverOK := serverConnection.(*net.TCPConn)
		if !clientOK || !serverOK {
			clientConnection.Close()
			serverConnection.Close()
			t.Fatal("TCP pair did not return TCP connections")
		}
		return server, client
	}

	return nil, nil
}

func emptyHTTPResponse(
	request *http.Request,
	status int,
) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       http.NoBody,
		Request:    request,
	}
}

func messageHTTPResponse(
	t *testing.T,
	request *http.Request,
	sessionID string,
	message jsonrpc.Message,
) *http.Response {
	t.Helper()

	data, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		t.Errorf("encode HTTP message: %v", err)
	}
	headers := http.Header{
		"Content-Type": {"application/json"},
	}
	if sessionID != "" {
		headers.Set(sessionIDHeader, sessionID)
	}

	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Request:    request,
	}
}

func TestResponseLimiterBoundsBodiesUntilClose(t *testing.T) {
	limiter := newResponseLimiter()
	bodies := make([]io.ReadCloser, 0, maxActiveResponseBodies)
	for range maxActiveResponseBodies {
		body, err := limiter.wrap(io.NopCloser(bytes.NewReader(nil)))
		if err != nil {
			t.Fatalf("wrap response body: %v", err)
		}
		bodies = append(bodies, body)
	}

	if _, err := limiter.wrap(io.NopCloser(bytes.NewReader(nil))); err == nil {
		t.Fatal("response limiter accepted one body above its limit")
	}
	if err := bodies[0].Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	replacement, err := limiter.wrap(io.NopCloser(bytes.NewReader(nil)))
	if err != nil {
		t.Fatalf("wrap replacement response body: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close replacement response body: %v", err)
	}
	for _, body := range bodies[1:] {
		if err := body.Close(); err != nil {
			t.Fatalf("close response body: %v", err)
		}
	}
}

func TestNewRejectsMessageLimitAboveSDKCeiling(t *testing.T) {
	_, err := New(Options{
		MaxMessageBytes: defaultMaxMessageSize + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("New error = %v, want SDK ceiling rejection", err)
	}
}

func TestDispatchReceiptReturnsCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	want := errors.New("dispatch canceled")
	cancel(want)

	err := newDispatchReceipt().wait(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("dispatch wait error = %v, want %v", err, want)
	}
}
