package control

// Peer is a JSON-RPC peer over a control connection: it writes newline-framed
// requests, matches responses back to their Call sites by id, and dispatches
// inbound requests to a Handler. Both host and sandbox sides run one.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

type Handler func(context.Context, []byte) ([]byte, error)

type Peer struct {
	conn    net.Conn
	handler Handler
	ctx     context.Context
	cancel  context.CancelFunc
	next    atomic.Int64

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan RPCResponse
	done    chan struct{}
	err     error
}

func NewPeer(ctx context.Context, conn net.Conn, handler Handler) *Peer {
	peerCtx, cancel := context.WithCancel(ctx)
	return &Peer{conn: conn, handler: handler, ctx: peerCtx, cancel: cancel, pending: map[string]chan RPCResponse{}, done: make(chan struct{})}
}

func (p *Peer) Start(reader *bufio.Reader) {
	if reader == nil {
		reader = bufio.NewReader(p.conn)
	}
	go p.readLoop(reader)
}

func (p *Peer) Close() error {
	p.cancel()
	return p.conn.Close()
}

func (p *Peer) Done() <-chan struct{} { return p.done }

func (p *Peer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Peer) Call(ctx context.Context, method string, params any) (RPCResponse, error) {
	id := p.next.Add(1)
	var raw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return RPCResponse{}, err
		}
		raw = data
	}
	request, err := NewRequest(id, method, raw)
	if err != nil {
		return RPCResponse{}, err
	}
	idBytes, _ := json.Marshal(id)
	key := string(idBytes)
	ch := make(chan RPCResponse, 1)
	p.mu.Lock()
	select {
	case <-p.done:
		err := p.err
		p.mu.Unlock()
		if err != nil {
			return RPCResponse{}, err
		}
		return RPCResponse{}, io.ErrClosedPipe
	default:
	}
	p.pending[key] = ch
	p.mu.Unlock()

	if err := p.writeLine(append(request, '\n')); err != nil {
		p.removePending(key)
		return RPCResponse{}, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return RPCResponse{}, resp.Error
		}
		return resp, nil
	case <-ctx.Done():
		p.removePending(key)
		return RPCResponse{}, ctx.Err()
	case <-p.done:
		err := p.Err()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return RPCResponse{}, err
	}
}

func (p *Peer) writeResponse(response []byte) error {
	return p.writeLine(response)
}

func (p *Peer) Respond(response []byte) error {
	return p.writeResponse(response)
}

func (p *Peer) writeLine(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.conn.Write(data)
	return err
}

func (p *Peer) readLoop(reader *bufio.Reader) {
	defer close(p.done)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			p.handleMessage(bytes.TrimSpace(line))
		}
		if err != nil {
			p.finish(err)
			return
		}
	}
}

func (p *Peer) handleMessage(data []byte) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		_ = p.writeResponse(ResponseError(nil, CodeParseError, err.Error(), nil))
		return
	}
	if probe.Method != "" {
		go p.handleRequest(data)
		return
	}
	resp, err := DecodeResponse(data)
	if err != nil {
		return
	}
	key := string(resp.ID)
	p.mu.Lock()
	ch := p.pending[key]
	delete(p.pending, key)
	p.mu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

func (p *Peer) handleRequest(data []byte) {
	if p.handler == nil {
		req, _ := DecodeRequest(data)
		_ = p.writeResponse(ResponseError(req.ID, CodeMethodNotFound, "method not found", nil))
		return
	}
	response, err := p.handler(p.ctx, data)
	if len(response) == 0 && err != nil {
		response = ResponseError(nil, CodeInternalError, err.Error(), nil)
	}
	if len(response) > 0 {
		_ = p.writeResponse(response)
	}
}

func (p *Peer) removePending(key string) {
	p.mu.Lock()
	delete(p.pending, key)
	p.mu.Unlock()
}

func (p *Peer) finish(err error) {
	p.cancel()
	p.mu.Lock()
	if err != nil && err != io.EOF {
		p.err = err
	}
	for key, ch := range p.pending {
		delete(p.pending, key)
		ch <- RPCResponse{JSONRPC: JSONRPCVersion, ID: json.RawMessage(key), Error: &RPCError{Code: CodeInternalError, Message: fmt.Sprint(io.ErrClosedPipe)}}
	}
	p.mu.Unlock()
}
