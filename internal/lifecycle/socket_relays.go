package lifecycle

// Collects host-socket relay requests from active concrete tools before the
// run-owned relay endpoints are created.

import (
	"fmt"

	"petris.dev/toby/internal/socketrelay"
	"petris.dev/toby/internal/tools"
)

// SocketRelays returns one validated immutable registry containing every
// contribution in tool dependency order.
func SocketRelays(set *tools.Toolset) (*socketrelay.Registry, error) {
	var requests []socketrelay.Request
	if set != nil {
		for _, tool := range set.OrderedTools() {
			contributor, ok := tool.(socketrelay.Contributor)
			if !ok {
				continue
			}

			contributed, err := contributor.SocketRelays()
			if err != nil {
				return nil, fmt.Errorf(
					"collect socket relays from %s: %w",
					tool.Name(),
					err,
				)
			}
			requests = append(requests, contributed...)
		}
	}

	registry, err := socketrelay.NewRegistry(requests)
	if err != nil {
		return nil, fmt.Errorf("validate tool socket relays: %w", err)
	}

	return registry, nil
}
