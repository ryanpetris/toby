package providers

// Registry collects the provider Clients registered via the fx "providers"
// group and dispatches each model lookup to the matching Client by Kind.

import (
	"context"
	"fmt"
)

// Registry indexes the registered provider Clients by Kind.
type Registry struct {
	byKind map[Kind]Client
}

// NewRegistry indexes the provider Clients supplied via the fx "providers" group.
func NewRegistry(clients []Client) *Registry {
	byKind := make(map[Kind]Client, len(clients))
	for _, client := range clients {
		byKind[client.Kind()] = client
	}

	return &Registry{byKind: byKind}
}

// LookupModels fetches a fresh model list for the provider of the given kind.
func (r *Registry) LookupModels(ctx context.Context, kind Kind, baseURL string, headers map[string]string) ([]Model, error) {
	client, ok := r.byKind[kind]
	if !ok {
		return nil, fmt.Errorf("no provider registered for kind %q", kind)
	}

	return client.LookupModels(ctx, baseURL, headers)
}
