package providergateway

// Declares non-secret models gateway construction sentinels.

import "errors"

var errRouteStoreRequired = errors.New(
	"provider route store is required",
)

// ErrConfigurationRejected marks a complete Caddy snapshot that cannot become
// valid through retry. The acquiring transaction removes its new routes,
// allowing reconciliation to restore the prior desired state.
var ErrConfigurationRejected = errors.New(
	"models gateway configuration was rejected",
)
