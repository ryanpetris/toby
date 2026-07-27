package resourcepool

// Defines process planning, startup, and ready-endpoint contracts around the
// generic agent resource registry.

import (
	"context"
	"fmt"
	"reflect"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/mcpgateway/localhttp"
	"petris.dev/toby/internal/mcpgateway/sidecar"
)

// Planner resolves an OCI reference and constructs the complete canonical
// resource identity without starting the process.
type Planner interface {
	// Plan resolves a definition into a complete launch plan.
	Plan(
		context.Context,
		localhttp.Definition,
		mcpgateway.ProgressReporter,
	) (Plan, error)
}

// Starter launches one exact resolved plan and returns only after its MCP
// endpoint is ready.
type Starter interface {
	// Start launches a resolved plan and waits for readiness.
	Start(context.Context, Plan, uint64) (Instance, error)
}

// Instance is one ready supervised process generation and its host-only MCP
// endpoint.
type Instance interface {
	resource.Instance
	// Upstream returns the instance's HTTP endpoint.
	Upstream() (httpbridge.Upstream, error)
}

// Plan pairs the builder input with the exact resolved launch definition.
// Resource must include resolved manifest/rootfs digests and every field that
// can affect process sharing.
type Plan struct {
	Resource     resource.Spec
	Definition   localhttp.Definition
	Capabilities *sidecar.MountCapabilities
}

var _ fmt.Stringer = Plan{}

// String redacts the complete process plan.
func (Plan) String() string {
	return "{Resource:<redacted> Definition:<redacted>}"
}

func isNilContract(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
