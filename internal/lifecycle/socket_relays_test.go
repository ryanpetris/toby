package lifecycle

// Verifies dependency-ordered socket-relay collection and cross-tool target
// collision rejection.

import (
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/socketrelay"
	"petris.dev/toby/internal/tools"
)

type socketRelayTool struct {
	tools.Base
	requests []socketrelay.Request
}

var _ tools.Tool = socketRelayTool{}
var _ socketrelay.Contributor = socketRelayTool{}

func (t socketRelayTool) SocketRelays() ([]socketrelay.Request, error) {
	return t.requests, nil
}

func TestSocketRelaysCollectsSelectedContributors(t *testing.T) {
	registry, err := tools.NewRegistry([]tools.Tool{
		socketRelayTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "docker"}},
			requests: []socketrelay.Request{{
				HostSocket:    "/var/run/docker.sock",
				SandboxSocket: layout.Runtime + "/docker.sock",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"docker"}, "docker")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := SocketRelays(set); err != nil {
		t.Fatal(err)
	}
}

func TestSocketRelaysRejectsCrossToolCollision(t *testing.T) {
	target := layout.Runtime + "/shared.sock"
	registry, err := tools.NewRegistry([]tools.Tool{
		socketRelayTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "first"}},
			requests: []socketrelay.Request{{
				HostSocket:    "/run/first.sock",
				SandboxSocket: target,
			}},
		},
		socketRelayTool{
			Base: tools.Base{Metadata: tools.Metadata{Name: "second"}},
			requests: []socketrelay.Request{{
				HostSocket:    "/run/second.sock",
				SandboxSocket: target,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"first", "second"}, "second")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := SocketRelays(set); err == nil {
		t.Fatal("cross-tool socket relay collision was accepted")
	}
}
