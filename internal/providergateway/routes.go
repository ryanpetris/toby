package providergateway

// Owns provider-route desired state, immediate authorization revocation, and
// deny tombstones awaiting confirmed Caddy removal.

import (
	"crypto/subtle"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
)

const maxGatewayRoutes = 4096

type storedRoute struct {
	route           route
	desired         bool
	active          bool
	removalRevision uint64
}

type routeSnapshot struct {
	Revision uint64
	Routes   []route
}

type routeStore struct {
	mu sync.RWMutex

	revision        uint64
	confirmed       uint64
	generationToken string
	routes          map[string]*storedRoute
	capabilities    map[string]string
}

func newRouteStore() *routeStore {
	return &routeStore{
		routes:       make(map[string]*storedRoute),
		capabilities: make(map[string]string),
	}
}

func (s *routeStore) add(routes []route) (uint64, error) {
	if s == nil {
		return 0, fmt.Errorf("provider route store is nil")
	}
	if len(routes) == 0 {
		return 0, fmt.Errorf("provider route set is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.routes)+len(routes) > maxGatewayRoutes {
		return 0, fmt.Errorf(
			"provider route count exceeds %d",
			maxGatewayRoutes,
		)
	}

	ids := make(map[string]struct{}, len(routes))
	capabilities := make(map[string]struct{}, len(routes))
	for index, item := range routes {
		if err := item.validate(); err != nil {
			return 0, fmt.Errorf(
				"provider route %d: %w",
				index,
				err,
			)
		}
		if _, exists := s.routes[item.ID]; exists {
			return 0, fmt.Errorf("provider route identity collision")
		}
		if _, exists := ids[item.ID]; exists {
			return 0, fmt.Errorf("provider route identity collision")
		}
		ids[item.ID] = struct{}{}

		if _, exists := s.capabilities[item.Capability]; exists {
			return 0, fmt.Errorf("provider route capability collision")
		}
		if _, exists := capabilities[item.Capability]; exists {
			return 0, fmt.Errorf("provider route capability collision")
		}
		capabilities[item.Capability] = struct{}{}
	}

	revision, err := s.nextRevisionLocked()
	if err != nil {
		return 0, err
	}
	for _, item := range routes {
		clone := item.clone()
		s.routes[item.ID] = &storedRoute{
			route:   clone,
			desired: true,
		}
		s.capabilities[item.Capability] = item.ID
	}

	return revision, nil
}

func (s *routeStore) activate(ids []string) error {
	if s == nil {
		return fmt.Errorf("provider route store is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		stored := s.routes[id]
		if stored == nil || !stored.desired {
			return fmt.Errorf(
				"provider route is no longer desired",
			)
		}
	}
	for _, id := range ids {
		s.routes[id].active = true
	}

	return nil
}

func (s *routeStore) revoke(ids []string) uint64 {
	if s == nil || len(ids) == 0 {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for _, id := range ids {
		stored := s.routes[id]
		if stored != nil && stored.desired {
			changed = true
			break
		}
	}
	if !changed {
		return s.revision
	}

	revision, err := s.nextRevisionLocked()
	if err != nil {
		// Revision exhaustion is unreachable in practice. Fail closed even
		// when a new reconciliation revision cannot be represented.
		for _, id := range ids {
			stored := s.routes[id]
			if stored != nil {
				stored.active = false
				stored.desired = false
				stored.removalRevision = math.MaxUint64
			}
		}
		return math.MaxUint64
	}
	for _, id := range ids {
		stored := s.routes[id]
		if stored == nil || !stored.desired {
			continue
		}

		stored.active = false
		stored.desired = false
		stored.removalRevision = revision
	}

	return revision
}

func (s *routeStore) snapshot() routeSnapshot {
	if s == nil {
		return routeSnapshot{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	routes := make([]route, 0, len(s.routes))
	for _, stored := range s.routes {
		if stored.desired {
			routes = append(routes, stored.route.clone())
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].ID < routes[j].ID
	})

	return routeSnapshot{
		Revision: s.revision,
		Routes:   routes,
	}
}

func (s *routeStore) confirm(revision uint64) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if revision <= s.confirmed || revision > s.revision {
		return
	}
	s.confirmed = revision

	for id, stored := range s.routes {
		if stored.desired ||
			stored.removalRevision == 0 ||
			stored.removalRevision > revision {
			continue
		}

		delete(s.routes, id)
		delete(s.capabilities, stored.route.Capability)
	}
}

func (s *routeStore) setGenerationToken(token string) error {
	if !validCapabilityToken(token, maxCredentialBytes) {
		return fmt.Errorf(
			"models gateway generation token is invalid",
		)
	}

	s.mu.Lock()
	s.generationToken = token
	s.mu.Unlock()

	return nil
}

func (s *routeStore) authorize(
	request *http.Request,
	allow func(),
) bool {
	if s == nil || request == nil || allow == nil {
		return false
	}
	if request.Method != http.MethodGet ||
		request.URL == nil ||
		request.URL.RawQuery != "" {
		return false
	}

	const prefix = "/authorize/"
	if len(request.URL.Path) <= len(prefix) ||
		request.URL.Path[:len(prefix)] != prefix {
		return false
	}
	id := request.URL.Path[len(prefix):]
	if !validCapabilityToken(id, maxCredentialBytes) {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	stored := s.routes[id]
	if stored == nil || !stored.desired || !stored.active {
		return false
	}
	if !singleHeaderEquals(
		request.Header,
		internalGatewayTokenHeader,
		s.generationToken,
	) ||
		!singleHeaderEquals(
			request.Header,
			internalCapabilityHeader,
			stored.route.Capability,
		) ||
		!stored.route.matchesCredential(request.Header) {
		return false
	}

	// The read lock stays held through the logical allow response. Revoke's
	// write lock therefore cannot return while a later authorization decision
	// is still able to succeed.
	allow()
	return true
}

func (s *routeStore) nextRevisionLocked() (uint64, error) {
	if s.revision == math.MaxUint64 {
		return 0, fmt.Errorf(
			"provider route revision space is exhausted",
		)
	}

	s.revision++
	return s.revision, nil
}

func singleHeaderEquals(
	header http.Header,
	name string,
	expected string,
) bool {
	if expected == "" {
		return false
	}

	values := header.Values(name)
	return len(values) == 1 &&
		constantTimeEqual(values[0], expected)
}

func constantTimeEqual(left string, right string) bool {
	return subtle.ConstantTimeCompare(
		[]byte(left),
		[]byte(right),
	) == 1
}
