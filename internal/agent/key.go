package agent

// Creates the ephemeral agent-local HMAC key used to fingerprint secrets in
// reusable resource identities without persisting or logging the key.

import (
	"crypto/rand"
	"fmt"

	"petris.dev/toby/internal/agent/resource"
)

const resourceHMACKeyBytes = 32

func newResourceBuilder() (*resource.Builder, error) {
	key := make([]byte, resourceHMACKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf(
			"generate agent resource HMAC key: %w",
			err,
		)
	}

	builder, err := resource.NewBuilder(key)
	clear(key)
	return builder, err
}
