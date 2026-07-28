//go:build linux

package sidecar

// Opens the verified per-user OCI, Bubblewrap, and transient-overlay services
// backing agent-owned local MCP sidecars.

import (
	"context"
	"errors"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/pasta"
)

type nativeRuntime struct {
	*Preparer

	images   *oci.Store
	storage  *bwrap.RunStorage
	executor *bwrap.Executor
	logger   *diagnostic.Logger
}

var _ Runtime = (*nativeRuntime)(nil)

// NewNativeLazy constructs an unopened per-user native sidecar runtime.
func NewNativeLazy(
	paths config.Paths,
	diagnostics *diagnostic.Service,
	network *pasta.Service,
) (*Lazy, error) {
	return newLazy(
		func(ctx context.Context) (Runtime, error) {
			return openNativeRuntime(ctx, paths, diagnostics, network)
		},
		diagnostics.Logger("mcp.sidecar"),
	)
}

func openNativeRuntime(
	ctx context.Context,
	paths config.Paths,
	diagnostics *diagnostic.Service,
	network *pasta.Service,
) (Runtime, error) {
	if ctx == nil {
		return nil, errors.New(
			"open native sidecar runtime: context is nil",
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{
		Logger: diagnostics.Logger("mcp.sidecar.bwrap"),
	})
	if err != nil {
		return nil, err
	}

	logger := diagnostics.Logger("mcp.sidecar")
	images, err := oci.NewStore(
		paths,
		diagnostics,
	)
	if err != nil {
		logger.DebugError("close Bubblewrap executor", executor.Close())
		return nil, err
	}
	storage, err := bwrap.OpenRunStorage(
		paths.RunStorageDir(),
		bwrap.DefaultRunStorageLimits(),
		logger,
	)
	if err != nil {
		logger.DebugError("close OCI image store", images.Close())
		logger.DebugError("close Bubblewrap executor", executor.Close())
		return nil, err
	}
	imageAdapter, err := NewOCI(images)
	if err != nil {
		logger.DebugError("close Bubblewrap run storage", storage.Close())
		logger.DebugError("close OCI image store", images.Close())
		logger.DebugError("close Bubblewrap executor", executor.Close())
		return nil, err
	}
	service, err := New(
		imageAdapter,
		storage,
		executor,
		network,
		logger,
	)
	if err != nil {
		logger.DebugError("close Bubblewrap run storage", storage.Close())
		logger.DebugError("close OCI image store", images.Close())
		logger.DebugError("close Bubblewrap executor", executor.Close())
		return nil, err
	}

	return &nativeRuntime{
		Preparer: service,
		images:   images,
		storage:  storage,
		executor: executor,
		logger:   logger,
	}, nil
}

func (r *nativeRuntime) Close() error {
	if r == nil {
		return nil
	}

	executor := r.executor
	storage := r.storage
	images := r.images
	r.Preparer = nil
	r.executor = nil
	r.storage = nil
	r.images = nil

	r.logger.DebugError("close Bubblewrap executor", executor.Close())
	r.logger.DebugError("close Bubblewrap run storage", storage.Close())
	r.logger.DebugError("close OCI image store", images.Close())
	return nil
}
