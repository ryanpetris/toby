package sidecar

// Adapts the per-user OCI store to native-platform sidecar preparation.

import (
	"context"
	"fmt"
	"runtime"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
)

// OCI prepares sidecar images through the verified per-user OCI store.
type OCI struct {
	store *oci.Store
}

var _ ImagePreparer = (*OCI)(nil)

// NewOCI constructs an OCI sidecar image adapter.
func NewOCI(store *oci.Store) (*OCI, error) {
	if store == nil {
		return nil, fmt.Errorf("sidecar OCI store is required")
	}

	return &OCI{store: store}, nil
}

// PrepareImage resolves the current Linux architecture and retains its exact
// immutable rootfs.
func (o *OCI) PrepareImage(
	ctx context.Context,
	reference string,
	progress mcpgateway.ProgressReporter,
) (Image, error) {
	if o == nil || o.store == nil {
		return nil, fmt.Errorf("sidecar OCI store is not configured")
	}

	prepared, err := o.store.Prepare(ctx, oci.Request{
		Reference: reference,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: image.PullIfMissing,
	})
	if err != nil {
		return nil, err
	}

	return prepared, nil
}
