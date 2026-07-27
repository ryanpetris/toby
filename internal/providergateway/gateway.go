package providergateway

// Owns the agent-lifetime authorization socket, loopback relay, desired
// routes, and active run acquisitions.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"petris.dev/toby/internal/diagnostic"
)

// Gateway coordinates one per-user models gateway.
type Gateway struct {
	routes     *routeStore
	relay      *relay
	auth       *authServer
	pool       Pool
	discoverer ModelDiscoverer
	options    Options
	logger     *diagnostic.Logger

	lifetime context.Context
	cancel   context.CancelFunc
	wake     chan struct{}

	mu              sync.Mutex
	active          map[*acquired]struct{}
	appliedRevision uint64
	failedRevision  uint64
	reconcileErr    error
	terminalErr     error
	changed         chan struct{}
	closing         bool
	progress        []*progressWaiter

	reconcileDone chan struct{}
	shutdownOnce  sync.Once
	shutdownDone  chan struct{}
	shutdownErr   error
}

// NewGateway opens the protected agent endpoints but does not start Caddy
// until a provider route is acquired.
func NewGateway(
	ctx context.Context,
	authSocket string,
	pool Pool,
	discoverer ModelDiscoverer,
	options Options,
) (*Gateway, error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"models gateway construction context is nil",
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if authSocket == "" ||
		!filepath.IsAbs(authSocket) ||
		filepath.Clean(authSocket) != authSocket {
		return nil, fmt.Errorf(
			"provider authorization socket must be a clean absolute path",
		)
	}
	if nilContract(pool) {
		return nil, fmt.Errorf("provider Caddy pool is required")
	}
	if nilContract(discoverer) {
		return nil, fmt.Errorf(
			"provider model discoverer is required",
		)
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	routes := newRouteStore()
	auth, err := newAuthServer(
		ctx,
		authSocket,
		routes,
		authServerOptions{Logger: normalized.Logger},
	)
	if err != nil {
		return nil, err
	}
	relay, err := newRelay(relayOptions{Logger: normalized.Logger})
	if err != nil {
		closeCtx, cancel := context.WithTimeout(
			context.Background(),
			normalized.CleanupTimeout,
		)
		defer cancel()
		normalized.Logger.DebugError(
			"close provider authorization service after initialization failure",
			auth.Close(closeCtx),
		)
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	gateway := &Gateway{
		routes:        routes,
		relay:         relay,
		auth:          auth,
		pool:          pool,
		discoverer:    discoverer,
		options:       normalized,
		logger:        normalized.Logger,
		lifetime:      lifetime,
		cancel:        cancel,
		wake:          make(chan struct{}, 1),
		active:        make(map[*acquired]struct{}),
		changed:       make(chan struct{}),
		reconcileDone: make(chan struct{}),
		shutdownDone:  make(chan struct{}),
	}
	go gateway.reconcile()
	go gateway.watchAuthorization()
	go gateway.watchRelay()

	return gateway, nil
}

// AuthorizationFile returns a caller-owned descriptor for the exact protected
// authorization socket. It is intended only for the Caddy sandbox launcher.
func (s *Gateway) AuthorizationFile() (*os.File, error) {
	if s == nil || s.auth == nil {
		return nil, fmt.Errorf(
			"provider authorization capability is unavailable",
		)
	}

	return s.auth.File()
}

// Shutdown revokes every route, closes ingress, and then reaps Caddy and the
// authorization endpoint.
func (s *Gateway) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf(
			"models gateway shutdown context is nil",
		)
	}

	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		active := make([]*acquired, 0, len(s.active))
		for item := range s.active {
			active = append(active, item)
		}
		s.notifyLocked()
		s.mu.Unlock()

		for _, item := range active {
			item.Revoke()
		}
		s.cancel()
		s.signal()

		go s.finishShutdown(active)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.shutdownDone:
		return s.shutdownErr
	}
}

func (s *Gateway) acquire(
	ctx context.Context,
	spec RequestSpec,
	progress ProgressReporter,
) (*acquired, error) {
	if s == nil {
		return nil, fmt.Errorf("models gateway is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf(
			"models gateway acquire context is nil",
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return nil, fmt.Errorf(
			"models gateway is shutting down",
		)
	}

	routes, err := buildRoutes(spec, s.options.NewToken)
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf(
			"models gateway request has no providers",
		)
	}
	installOperation := startProgress(
		progress,
		ResourceKind,
		"Installing models routes",
	)
	failInstall := func(cause error) error {
		installOperation.Fail("Models route installation failed")
		return cause
	}

	descriptorSlots := make([]*ProviderDescriptor, len(routes))
	for index, item := range routes {
		descriptor, err := newProviderDescriptor(
			item,
			s.relay.baseURL(),
			map[string]any{},
		)
		if err != nil {
			return nil, failInstall(err)
		}
		descriptorSlots[index] = &descriptor
	}
	preflightConfig := descriptorConfigFromSlots(descriptorSlots)
	if _, err := encodeProviderDescriptorConfig(preflightConfig); err != nil {
		return nil, failInstall(err)
	}

	revision, err := s.routes.add(routes)
	if err != nil {
		return nil, failInstall(err)
	}
	progressWaiter := s.registerProgress(revision, progress)
	defer s.unregisterProgress(progressWaiter)
	ids := routeIDs(routes)
	rollback := func(cause error) error {
		removal := s.revoke(ids)
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			s.options.CleanupTimeout,
		)
		defer cancel()
		s.logger.DebugError(
			"remove models routes after installation failure",
			s.waitRemoval(cleanupCtx, removal),
		)
		return cause
	}

	s.signal()
	if err := s.waitRevision(ctx, revision); err != nil {
		return nil, failInstall(rollback(err))
	}
	if err := s.routes.activate(ids); err != nil {
		return nil, failInstall(rollback(err))
	}
	installOperation.Complete("Models routes installed")

	descriptorConfig := descriptorConfigFromSlots(descriptorSlots)
	if _, err := encodeProviderDescriptorConfig(descriptorConfig); err != nil {
		return nil, rollback(err)
	}

	result := &acquired{
		gateway:     s,
		routeIDs:    append([]string(nil), ids...),
		descriptor:  descriptorConfig.clone(),
		releaseDone: make(chan struct{}),
	}
	if err := s.register(result); err != nil {
		return nil, rollback(err)
	}

	return result, nil
}

func (s *Gateway) register(item *acquired) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closing {
		return fmt.Errorf("models gateway is shutting down")
	}
	s.active[item] = struct{}{}

	return nil
}

func (s *Gateway) unregister(item *acquired) {
	s.mu.Lock()
	delete(s.active, item)
	s.mu.Unlock()
}

func (s *Gateway) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closing
}

func (s *Gateway) revoke(ids []string) uint64 {
	revision := s.routes.revoke(ids)
	s.signal()

	return revision
}

func (s *Gateway) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Gateway) watchAuthorization() {
	select {
	case <-s.lifetime.Done():
		return
	case <-s.auth.Done():
	}

	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.terminalErr = fmt.Errorf(
			"provider authorization service stopped",
		)
		s.notifyLocked()
	}
	s.mu.Unlock()
	s.cancel()
	s.signal()
}

func (s *Gateway) watchRelay() {
	select {
	case <-s.lifetime.Done():
		return
	case <-s.relay.Done():
	}

	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.terminalErr = s.relay.Err()
		if s.terminalErr == nil {
			s.terminalErr = fmt.Errorf(
				"models gateway relay stopped",
			)
		}
		s.notifyLocked()
	}
	s.mu.Unlock()
	s.cancel()
	s.signal()
}

func (s *Gateway) finishShutdown(active []*acquired) {
	defer close(s.shutdownDone)

	cleanupCtx, cancel := context.WithTimeout(
		context.Background(),
		s.options.CleanupTimeout,
	)
	defer cancel()

	for _, item := range active {
		s.logger.DebugError(
			"release models gateway route",
			item.Release(cleanupCtx),
		)
	}
	s.logger.DebugError("close models gateway relay", s.relay.Close())
	<-s.reconcileDone
	s.logger.DebugError(
		"shut down Caddy models gateway",
		s.pool.Shutdown(cleanupCtx),
	)
	s.logger.DebugError(
		"shut down provider authorization service",
		s.auth.Close(cleanupCtx),
	)

	s.shutdownErr = nil
}

func (s *Gateway) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func nilContract(value any) bool {
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

func routeIDs(routes []route) []string {
	ids := make([]string, len(routes))
	for index, item := range routes {
		ids[index] = item.ID
	}

	return ids
}
