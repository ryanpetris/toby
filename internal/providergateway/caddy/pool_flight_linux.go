//go:build linux

package caddy

// Joins concurrent Caddy runtime initialization and bounded pool shutdown.

import "context"

type poolFlight struct {
	done chan struct{}
	err  error
}

func (p *Pool) finishShutdown(flight *poolFlight) {
	defer close(p.shutdownDone)
	if flight != nil {
		<-flight.done
	}

	p.mu.Lock()
	native := p.runtime
	p.runtime = nil
	p.mu.Unlock()
	if native != nil {
		p.shutdownErr = native.Close(context.Background())
	}
}
