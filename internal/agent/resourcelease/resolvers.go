package resourcelease

// Applies resource-specific typed defaults before agent-only canonical
// identity hashing.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config/mcpresource"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/resourcehash"
)

type typedResolver[T any] struct {
	kind      protocol.ResourceKind
	hashes    *resourcehash.Service
	normalize func(T) (T, error)
}

// NewMCPResolver constructs the agent resolver for one MCP resource.
func NewMCPResolver(
	hashes *resourcehash.Service,
) (Resolver, error) {
	return newTypedResolver(
		protocol.ResourceMCP,
		hashes,
		mcpresource.Normalize,
	)
}

// NewModelsResolver constructs the agent resolver for one models API
// resource.
func NewModelsResolver(
	hashes *resourcehash.Service,
) (Resolver, error) {
	return newTypedResolver(
		protocol.ResourceModels,
		hashes,
		modelsconfig.Normalize,
	)
}

// NewOCIResolver constructs the agent resolver for one OCI image resource.
func NewOCIResolver(
	hashes *resourcehash.Service,
) (Resolver, error) {
	return newTypedResolver(
		protocol.ResourceOCI,
		hashes,
		ociresource.Normalize,
	)
}

func newTypedResolver[T any](
	kind protocol.ResourceKind,
	hashes *resourcehash.Service,
	normalize func(T) (T, error),
) (*typedResolver[T], error) {
	if hashes == nil {
		return nil, fmt.Errorf("resource hashing service is required")
	}
	if normalize == nil {
		return nil, fmt.Errorf(
			"resource normalization service is required",
		)
	}

	return &typedResolver[T]{
		kind:      kind,
		hashes:    hashes,
		normalize: normalize,
	}, nil
}

func (r *typedResolver[T]) Kind() protocol.ResourceKind {
	return r.kind
}

func (r *typedResolver[T]) Resolve(
	ctx context.Context,
	raw json.RawMessage,
) (Resolved, error) {
	if ctx == nil {
		return Resolved{}, fmt.Errorf("resource resolution context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Resolved{}, err
	}

	var input T
	if err := decodeTypedConfiguration(raw, &input); err != nil {
		return Resolved{}, err
	}
	effective, err := r.normalize(input)
	if err != nil {
		return Resolved{}, err
	}
	digest, err := r.hashes.Sum(struct {
		Schema        int                   `json:"schema"`
		Kind          protocol.ResourceKind `json:"kind"`
		Configuration T                     `json:"configuration"`
	}{
		Schema:        1,
		Kind:          r.kind,
		Configuration: effective,
	})
	if err != nil {
		return Resolved{}, err
	}

	return Resolved{
		ID:            protocol.ResourceID(digest.UUID()),
		Digest:        digest,
		Kind:          r.kind,
		Configuration: effective,
	}, nil
}

func decodeTypedConfiguration(
	raw json.RawMessage,
	destination any,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode resource configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("decode resource configuration: %w", err)
	}

	return nil
}
