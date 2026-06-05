package providers

// Registry collects the provider Clients registered via the fx "providers" group,
// dispatches model lookups to the matching Client by Kind, and memoizes results.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry indexes the registered provider Clients by Kind, dispatches model
// lookups to the matching one, and memoizes successful results so repeated
// lookups for the same endpoint do not hit the network again.
type Registry struct {
	byKind map[Kind]Client

	mu    sync.Mutex
	cache map[string][]Model
}

// NewRegistry indexes the provider Clients supplied via the fx "providers" group.
func NewRegistry(clients []Client) *Registry {
	byKind := make(map[Kind]Client, len(clients))
	for _, client := range clients {
		byKind[client.Kind()] = client
	}

	return &Registry{byKind: byKind, cache: map[string][]Model{}}
}

// LookupModels fetches the models for the provider of the given kind, caching the
// result keyed by kind, baseURL, and headers. Concurrent and subsequent calls
// with the same arguments return the cached slice. Errors are not cached.
func (r *Registry) LookupModels(ctx context.Context, kind Kind, baseURL string, headers map[string]string) ([]Model, error) {
	key := cacheKey(kind, baseURL, headers)

	r.mu.Lock()
	cached, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	client, ok := r.byKind[kind]
	if !ok {
		return nil, fmt.Errorf("no provider registered for kind %q", kind)
	}

	models, err := client.LookupModels(ctx, baseURL, headers)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[key] = models
	r.mu.Unlock()

	return models, nil
}

func cacheKey(kind Kind, baseURL string, headers map[string]string) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(string(kind))
	b.WriteByte(0)
	b.WriteString(baseURL)
	for _, name := range names {
		b.WriteByte(0)
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(headers[name])
	}

	return b.String()
}
