// Package resourcehandler implements agent-owned OCI preparation resources.
package resourcehandler

// Coordinates agent-owned OCI preparation operations without mutating image
// storage until a client requests preparation.

import (
	"context"
	"fmt"
	"io"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
)

const (
	defaultMaximumParallel = 4
	defaultMaximumLogBytes = 64 << 20
)

type imageBackend interface {
	Prepare(context.Context, oci.Request) (io.Closer, error)
	Close() error
}

type backendFactory func(config.Paths, *diagnostic.Service) (imageBackend, error)

type nativeImageBackend struct {
	store *oci.Store
}

func (b nativeImageBackend) Prepare(
	ctx context.Context,
	request oci.Request,
) (io.Closer, error) {
	return b.store.Prepare(ctx, request)
}

func (b nativeImageBackend) Close() error {
	return b.store.Close()
}

// Options bounds concurrent preparation work and each disk-backed operation
// log. Zero values select production defaults.
type Options struct {
	MaximumParallel int
	MaximumLogBytes int64

	newBackend backendFactory
}

func (o Options) withDefaults() (Options, error) {
	if o.MaximumParallel < 0 || o.MaximumLogBytes < 0 {
		return Options{}, fmt.Errorf(
			"OCI resource service limits must not be negative",
		)
	}
	if o.MaximumParallel == 0 {
		o.MaximumParallel = defaultMaximumParallel
	}
	if o.MaximumLogBytes == 0 {
		o.MaximumLogBytes = defaultMaximumLogBytes
	}
	if o.newBackend == nil {
		o.newBackend = func(
			paths config.Paths,
			diagnostics *diagnostic.Service,
		) (imageBackend, error) {
			store, err := oci.NewStore(paths, diagnostics)
			if err != nil {
				return nil, err
			}

			return nativeImageBackend{store: store}, nil
		}
	}

	return o, nil
}

// Service owns active OCI preparations and their disk-backed replay logs.
type Service struct {
	paths       config.Paths
	diagnostics *diagnostic.Service
	logger      *diagnostic.Logger
	options     Options
	permits     chan struct{}
	logs        *resourcelog.Service

	lifetime context.Context
	cancel   context.CancelFunc

	initMu sync.Mutex
	images imageBackend

	mu       sync.Mutex
	active   map[protocol.ResourceID]*operation
	creating map[protocol.ResourceID]chan struct{}
	closing  bool
	workers  sync.WaitGroup
}

var _ resourcelease.RuntimeLifecycle = (*Service)(nil)
var _ resourcelease.RuntimeLister = (*Service)(nil)

// New constructs a dormant OCI resource service.
func New(
	paths config.Paths,
	logs *resourcelog.Service,
	diagnostics *diagnostic.Service,
	options Options,
) (*Service, error) {
	if logs == nil {
		return nil, fmt.Errorf("OCI resource log service is required")
	}
	options, err := options.withDefaults()
	if err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())

	return &Service{
		paths:       paths,
		diagnostics: diagnostics,
		logger:      diagnostics.Logger("oci.resource"),
		options:     options,
		permits:     make(chan struct{}, options.MaximumParallel),
		logs:        logs,
		lifetime:    lifetime,
		cancel:      cancel,
		active:      make(map[protocol.ResourceID]*operation),
		creating:    make(map[protocol.ResourceID]chan struct{}),
	}, nil
}

// Kind reports the resource kind served by this service.
func (*Service) Kind() protocol.ResourceKind {
	return protocol.ResourceOCI
}

// RuntimeResourceIDs returns opaque identities for active OCI preparation
// operations, including operations continuing after their final lease closes.
func (h *Service) RuntimeResourceIDs() []protocol.ResourceID {
	if h == nil {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]protocol.ResourceID, 0, len(h.active))
	for id := range h.active {
		result = append(result, id)
	}

	return result
}

// LeaseAcquired records no runtime demand because OCI registrations remain
// dormant until their preparation stream receives a request.
func (*Service) LeaseAcquired(resourcelease.Resolved) {}

// LeaseReleased does not cancel shared preparation work needed by other or
// immediately following clients.
func (*Service) LeaseReleased(resourcelease.Resolved) {}

// Open validates the agent-private configuration and returns one request
// stream without starting preparation.
func (h *Service) Open(
	ctx context.Context,
	request resourcelease.StreamRequest,
) (agentserver.ResourceStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("OCI resource stream context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, ok := request.Resource.Configuration.(ociresource.Config)
	if !ok {
		return nil, fmt.Errorf(
			"OCI resource has configuration type %T",
			request.Resource.Configuration,
		)
	}

	h.mu.Lock()
	closing := h.closing
	h.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("OCI resource service is shutting down")
	}

	return &resourceStream{
		service:       h,
		resourceID:    request.Resource.ID,
		configuration: configuration,
	}, nil
}

func (h *Service) operation(
	resourceID protocol.ResourceID,
	configuration ociresource.Config,
) (*operation, error) {
	for {
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			return nil, fmt.Errorf("OCI resource service is shutting down")
		}
		if current := h.active[resourceID]; current != nil {
			current.retain()
			h.mu.Unlock()
			return current, nil
		}
		if wait := h.creating[resourceID]; wait != nil {
			h.mu.Unlock()
			select {
			case <-h.lifetime.Done():
				return nil, h.lifetime.Err()
			case <-wait:
				continue
			}
		}

		wait := make(chan struct{})
		h.creating[resourceID] = wait
		h.mu.Unlock()

		operationID := protocol.NewOperationID()
		logFile := h.createOperationLog(resourceID, operationID)

		h.mu.Lock()
		delete(h.creating, resourceID)
		close(wait)
		current := newOperation(
			operationID,
			logFile,
			h.options.MaximumLogBytes,
			h.logger,
		)
		current.retain()
		if h.closing {
			h.mu.Unlock()
			h.logger.DebugError(
				"close unused OCI operation",
				current.close(),
				"resource_id",
				resourceID,
				"operation_id",
				operationID,
			)
			return nil, fmt.Errorf(
				"OCI resource service is shutting down",
			)
		}
		h.active[resourceID] = current
		h.workers.Add(1)
		h.mu.Unlock()

		go h.prepare(resourceID, configuration, current)

		return current, nil
	}
}

func (h *Service) prepare(
	resourceID protocol.ResourceID,
	configuration ociresource.Config,
	current *operation,
) {
	defer h.workers.Done()
	defer func() {
		h.mu.Lock()
		if h.active[resourceID] == current {
			delete(h.active, resourceID)
		}
		h.mu.Unlock()
		current.producerDone()
	}()

	select {
	case h.permits <- struct{}{}:
		defer func() { <-h.permits }()
	case <-h.lifetime.Done():
		current.fail(h.lifetime.Err())
		return
	}

	images, err := h.imageBackend()
	if err != nil {
		current.fail(err)
		return
	}

	prepared, err := images.Prepare(h.lifetime, oci.Request{
		Reference:  configuration.Reference,
		Platform:   configuration.Platform,
		PullPolicy: configuration.PullPolicy,
		Progress:   current.report,
	})
	if err != nil {
		current.fail(err)
		return
	}
	if prepared == nil {
		current.fail(fmt.Errorf("OCI preparation returned no image"))
		return
	}
	h.logger.DebugError(
		"close prepared OCI image",
		prepared.Close(),
		"operation_id", current.id,
	)

	current.complete()
}

func (h *Service) imageBackend() (imageBackend, error) {
	h.initMu.Lock()
	defer h.initMu.Unlock()

	if h.images != nil {
		return h.images, nil
	}
	h.mu.Lock()
	closing := h.closing
	h.mu.Unlock()
	if closing {
		return nil, fmt.Errorf("OCI resource service is shutting down")
	}

	images, err := h.options.newBackend(h.paths, h.diagnostics)
	if err != nil {
		return nil, fmt.Errorf("open agent OCI image store: %w", err)
	}
	h.images = images

	return images, nil
}

// Shutdown cancels preparation, joins all workers, and closes retained
// filesystem capabilities.
func (h *Service) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return nil
	}
	h.closing = true
	h.cancel()
	h.mu.Unlock()

	workersDone := make(chan error, 1)
	go func() {
		h.workers.Wait()

		h.initMu.Lock()
		images := h.images
		h.images = nil
		h.initMu.Unlock()

		if images == nil {
			workersDone <- nil
			return
		}
		workersDone <- images.Close()
	}()
	select {
	case err := <-workersDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
