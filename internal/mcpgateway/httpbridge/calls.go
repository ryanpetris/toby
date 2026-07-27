package httpbridge

// Bounds downstream calls until matching upstream responses are observed.

import (
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

const maxOutstandingCalls = 64

type callTracker struct {
	mu    sync.Mutex
	calls map[jsonrpc.ID]struct{}
}

func newCallTracker() *callTracker {
	return &callTracker{
		calls: make(map[jsonrpc.ID]struct{}),
	}
}

func (t *callTracker) acquire(
	message jsonrpc.Message,
) (jsonrpc.ID, bool, error) {
	request, ok := message.(*jsonrpc.Request)
	if !ok || !request.ID.IsValid() {
		return jsonrpc.ID{}, false, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, duplicate := t.calls[request.ID]; duplicate {
		return jsonrpc.ID{}, false, errors.New(
			"MCP HTTP session reused an outstanding request ID",
		)
	}
	if len(t.calls) >= maxOutstandingCalls {
		return jsonrpc.ID{}, false, errors.New(
			"MCP HTTP session exceeded the outstanding call limit",
		)
	}

	t.calls[request.ID] = struct{}{}
	return request.ID, true, nil
}

func (t *callTracker) complete(message jsonrpc.Message) {
	response, ok := message.(*jsonrpc.Response)
	if !ok || !response.ID.IsValid() {
		return
	}

	t.release(response.ID)
}

func (t *callTracker) release(id jsonrpc.ID) {
	t.mu.Lock()
	delete(t.calls, id)
	t.mu.Unlock()
}
