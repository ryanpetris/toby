// The home manager runs in the per-profile home container (as root, mounting the
// shared home volume). It dials the daemon over stdio and serves the Control stream —
// the files capability (write context/config into the home) and the exec capability
// (run installs/upgrades/init as the invoking user, streaming output back). It binds
// no proxy listener; MCP/provider proxying is the netns manager's job.

package sandbox

import (
	"context"
	"log"
	"os"

	"petris.dev/toby/internal/control"
	execcap "petris.dev/toby/internal/control/methods/exec"
	filescap "petris.dev/toby/internal/control/methods/files"
	"petris.dev/toby/internal/control/stdio"
	"petris.dev/toby/internal/control/tunnel"
)

// HomeRunner runs the files + exec manager.
type HomeRunner struct{}

func NewHomeRunner() *HomeRunner { return &HomeRunner{} }

// Run dials the daemon, reports readiness, and serves the Control stream (files + exec)
// until the context is cancelled.
func (r *HomeRunner) Run(ctx context.Context) error {
	log.SetOutput(os.Stderr) // fd 1 carries gRPC frames

	link := stdio.NewConn(os.Stdin, os.Stdout, nil)
	cc, client, err := tunnel.Dial(link)
	if err != nil {
		return err
	}
	defer cc.Close()

	// Signal readiness (no proxy address — the home manager has no listener); the
	// daemon's onReady gate fires so it can start issuing file/exec calls.
	if _, err := client.Ready(ctx, &tunnel.ReadyRequest{Addr: "home"}); err != nil {
		return err
	}

	stream, err := client.Control(ctx)
	if err != nil {
		return err
	}
	conn := tunnel.NewStreamConn(stream, nil)

	var peer *control.Peer
	execCap := execcap.New(func(method string, params any) error { return peer.Notify(method, params) })
	router, err := control.NewRouter([]control.Capability{filescap.New(), execCap})
	if err != nil {
		return err
	}
	handler := func(hctx context.Context, data []byte) ([]byte, error) {
		req, decErr := control.DecodeRequest(data)
		if decErr != nil {
			return control.ResponseError(nil, control.CodeInvalidRequest, decErr.Error(), nil), decErr
		}
		return router.Handle(hctx, req)
	}
	peer = control.NewPeer(ctx, conn, handler)
	peer.Start(nil)

	select {
	case <-peer.Done():
		return peer.Err()
	case <-ctx.Done():
		_ = peer.Close()
		return ctx.Err()
	}
}
