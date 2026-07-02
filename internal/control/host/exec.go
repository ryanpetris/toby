// Streamed exec over the home manager's Control stream: ExecStream issues exec.run and
// registers a chunk sink keyed by a generated exec id; HandleControl (wired as the
// tunnel server's Control handler) dispatches the manager's exec.output notifications
// to the matching sink. This lets install/upgrade output stream from the sandbox to
// the daemon (and on to the client) while the call blocks for the exit code.

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"petris.dev/toby/internal/control"
	execcap "petris.dev/toby/internal/control/methods/exec"
)

// ChunkFunc receives one streamed output chunk.
type ChunkFunc func(stream string, data []byte)

type execDispatch struct {
	mu      sync.Mutex
	sinks   map[string]ChunkFunc
	counter atomic.Uint64
}

func newExecDispatch() *execDispatch {
	return &execDispatch{sinks: map[string]ChunkFunc{}}
}

func (d *execDispatch) register(id string, fn ChunkFunc) {
	d.mu.Lock()
	d.sinks[id] = fn
	d.mu.Unlock()
}

func (d *execDispatch) unregister(id string) {
	d.mu.Lock()
	delete(d.sinks, id)
	d.mu.Unlock()
}

func (d *execDispatch) deliver(id, stream string, data []byte) {
	d.mu.Lock()
	fn := d.sinks[id]
	d.mu.Unlock()
	if fn != nil {
		fn(stream, data)
	}
}

// ExecStream runs argv in the sandbox as uid:gid, streaming stdout/stderr to onChunk,
// and returns the exit code.
func (c *SandboxClient) ExecStream(ctx context.Context, argv, env []string, cwd string, uid, gid int, onChunk ChunkFunc) (int, error) {
	uid, gid, err := resolveOwner(uid, gid)
	if err != nil {
		return 1, err
	}
	id := fmt.Sprintf("e-%d", c.exec.counter.Add(1))
	if onChunk != nil {
		c.exec.register(id, onChunk)
		defer c.exec.unregister(id)
	}
	resp, err := c.caller.Call(ctx, execcap.MethodRun, execcap.RunParams{
		ExecID: id, Argv: argv, Env: env, Cwd: cwd, UID: uid, GID: gid,
	})
	if err != nil {
		return 1, err
	}
	var result execcap.RunResult
	if resp.Result != nil {
		data, _ := json.Marshal(resp.Result)
		_ = json.Unmarshal(data, &result)
	}
	return result.ExitCode, nil
}

// HandleControl dispatches inbound Control-stream messages (exec.output notifications)
// from the manager. It returns no response — these are one-way notifications.
func (c *SandboxClient) HandleControl(_ context.Context, data []byte) ([]byte, error) {
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, nil
	}
	if msg.Method == execcap.MethodOutput {
		var out execcap.OutputParams
		if err := json.Unmarshal(msg.Params, &out); err == nil {
			c.exec.deliver(out.ExecID, out.Stream, out.Data)
		}
	}
	return nil, nil
}

var _ control.Handler = (*SandboxClient)(nil).HandleControl
