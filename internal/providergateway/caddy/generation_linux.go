//go:build linux

package caddy

// Adapts one retained Caddy resource lease to a models gateway generation.

import (
	"context"
	"fmt"
	"net"
	"sync"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/providergateway"
)

type generation struct {
	lease    *resource.Lease
	instance *Instance
	once     sync.Once
}

var _ providergateway.Generation = (*generation)(nil)

func (g *generation) Generation() uint64 {
	if g == nil || g.lease == nil {
		return 0
	}

	return g.lease.Generation()
}

func (g *generation) DialData(
	ctx context.Context,
) (*net.UnixConn, error) {
	if g == nil || g.instance == nil {
		return nil, fmt.Errorf("caddy generation is unavailable")
	}

	return g.instance.DialData(ctx)
}

func (g *generation) Load(ctx context.Context, config []byte) error {
	if g == nil || g.instance == nil {
		return fmt.Errorf("caddy generation is unavailable")
	}

	return g.instance.Load(ctx, config)
}

func (g *generation) OpenConnector() (
	providergateway.Connector,
	error,
) {
	if g == nil || g.lease == nil {
		return nil, resource.ErrLeaseClosed
	}

	return g.lease.OpenConnector()
}

func (g *generation) Done() <-chan struct{} {
	if g == nil || g.lease == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return g.lease.Done()
}

func (g *generation) Err() error {
	if g == nil || g.lease == nil {
		return nil
	}

	return g.lease.Err()
}

func (g *generation) Release() {
	if g == nil || g.lease == nil {
		return
	}

	g.once.Do(g.lease.Release)
}
