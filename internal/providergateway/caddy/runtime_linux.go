//go:build linux

package caddy

// Opens and closes the OCI, Bubblewrap, and resource-registry runtime.

import (
	"context"
	"fmt"
	"os"
	"runtime"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/providergateway"
	"petris.dev/toby/internal/sandbox/bwrap"
)

func (p *Pool) openRuntime(
	ctx context.Context,
	progress providergateway.ProgressReporter,
) (result *nativeRuntime, returnErr error) {
	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{
		Logger: p.logger,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close Caddy executor after initialization failure",
				executor.Close(),
			)
		}
	}()

	uid, gid := os.Geteuid(), os.Getegid()
	imageStoreOperation := startProgress(
		progress,
		"Opening Caddy image store",
	)
	images, err := oci.NewStore(
		p.paths,
		p.diagnostics,
	)
	if err != nil {
		imageStoreOperation.Fail("Opening Caddy image store failed")
		return nil, err
	}
	imageStoreOperation.Complete("Caddy image store ready")
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close Caddy image store after initialization failure",
				images.Close(),
			)
		}
	}()

	runStorageOperation := startProgress(
		progress,
		"Opening Caddy run storage",
	)
	storage, err := bwrap.OpenRunStorage(
		p.paths.RunStorageDir(),
		bwrap.DefaultRunStorageLimits(),
		p.logger,
	)
	if err != nil {
		runStorageOperation.Fail("Opening Caddy run storage failed")
		return nil, err
	}
	runStorageOperation.Complete("Caddy run storage ready")
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close Caddy run storage after initialization failure",
				storage.Close(),
			)
		}
	}()

	imageOperation := startProgress(
		progress,
		"Preparing Caddy OCI image",
	)
	prepared, err := images.Prepare(ctx, oci.Request{
		Reference: p.image,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: image.PullIfMissing,
	})
	if err != nil {
		imageOperation.Fail("Caddy OCI image preparation failed")
		return nil, err
	}
	imageOperation.Complete("Caddy OCI image ready")
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close prepared Caddy image after initialization failure",
				prepared.Close(),
			)
		}
	}()

	auth, err := p.authSource()
	if err != nil {
		return nil, fmt.Errorf(
			"open Caddy authorization capability",
		)
	}
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close Caddy authorization source after initialization failure",
				auth.Close(),
			)
		}
	}()

	resolver, err := os.Open(defaultResolverSource)
	if err != nil {
		return nil, fmt.Errorf(
			"open Caddy resolver capability",
		)
	}
	defer func() {
		if returnErr != nil {
			p.logger.DebugError(
				"close Caddy resolver source after initialization failure",
				resolver.Close(),
			)
		}
	}()

	spec, err := p.resourceSpec(prepared, auth, resolver)
	if err != nil {
		return nil, err
	}
	key, err := p.builder.Build(spec)
	if err != nil {
		return nil, err
	}

	factory := &factory{
		image:          prepared,
		storage:        storage,
		executor:       executor,
		authPath:       p.authPath,
		auth:           auth,
		resolver:       resolver,
		readinessLimit: p.options.ReadinessTimeout,
		readinessPoll:  p.options.ReadinessPoll,
		uid:            uid,
		gid:            gid,
		logger:         p.logger,
	}
	registry, err := resource.NewRegistry(
		factory,
		resource.Options{
			IdleTimeout: p.options.IdleTimeout,
		},
	)
	if err != nil {
		return nil, err
	}

	return &nativeRuntime{
		registry: registry,
		image:    prepared,
		storage:  storage,
		images:   images,
		executor: executor,
		auth:     auth,
		resolver: resolver,
		key:      key,
		logger:   p.logger,
	}, nil
}

type nativeRuntime struct {
	registry *resource.Registry
	image    *oci.Prepared
	storage  *bwrap.RunStorage
	images   *oci.Store
	executor *bwrap.Executor
	auth     *os.File
	resolver *os.File
	key      resource.Key
	logger   *diagnostic.Logger
}

func (r *nativeRuntime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}

	shutdownErr := r.registry.Shutdown(ctx)
	r.logger.DebugError("close Caddy authorization source", r.auth.Close())
	r.logger.DebugError("close Caddy resolver source", r.resolver.Close())
	r.logger.DebugError("close prepared Caddy image", r.image.Close())
	r.logger.DebugError("close Caddy run storage", r.storage.Close())
	r.logger.DebugError("close Caddy image store", r.images.Close())
	r.logger.DebugError("close Caddy executor", r.executor.Close())
	return shutdownErr
}
