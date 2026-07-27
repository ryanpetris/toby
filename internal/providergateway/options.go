package providergateway

// Defines finite models-gateway lifecycle, retry, and capability-generation
// policy.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultCleanupTimeout = 15 * time.Second
	defaultRetryDelay     = 100 * time.Millisecond
	randomTokenBytes      = 32
)

// Options controls bounded gateway cleanup and reconciliation. Zero values use
// production defaults.
type Options struct {
	CleanupTimeout time.Duration
	RetryDelay     time.Duration
	NewToken       func() (string, error)
	Logger         *diagnostic.Logger
}

func (o Options) normalized() (Options, error) {
	if o.CleanupTimeout < 0 {
		return Options{}, fmt.Errorf(
			"models gateway cleanup timeout must not be negative",
		)
	}
	if o.RetryDelay < 0 {
		return Options{}, fmt.Errorf(
			"models gateway retry delay must not be negative",
		)
	}
	if o.CleanupTimeout == 0 {
		o.CleanupTimeout = defaultCleanupTimeout
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = defaultRetryDelay
	}
	if o.NewToken == nil {
		o.NewToken = randomGatewayToken
	}

	return o, nil
}

func randomGatewayToken() (string, error) {
	var value [randomTokenBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf(
			"generate models gateway capability",
		)
	}

	return hex.EncodeToString(value[:]), nil
}
