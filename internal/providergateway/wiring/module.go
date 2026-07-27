package wiring

// Provides model clients, the lazy shared Caddy gateway, and the agent models
// resource service.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/fx"

	"petris.dev/toby/internal/agent/progressio"
	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providergateway"
	"petris.dev/toby/internal/providergateway/caddy"
	"petris.dev/toby/internal/providergateway/modelresource"
	"petris.dev/toby/internal/providers"
	"petris.dev/toby/internal/providers/anthropic"
	"petris.dev/toby/internal/providers/openai"
)

// Module provides process-wide models-gateway composition. Listeners, OCI stores,
// Bubblewrap, and Caddy remain unopened until the first provider acquisition.
func Module() fx.Option {
	return fx.Module(
		"providergateway",
		providers.Module(),
		openai.Module(),
		anthropic.Module(),
		fx.Provide(
			newDiscoveryHTTPClient,
			newProductionResolver,
			newModelsResourceService,
		),
	)
}

func newDiscoveryHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		MaxConnsPerHost:       16,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(
			*http.Request,
			[]*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}
}

type modelsOpenerResult struct {
	fx.Out

	Opener resourcelease.ResourceOpener `group:"resourceOpeners"`
}

func newModelsResourceService(
	resolver *providergateway.Resolver,
	logs *resourcelog.Service,
	diagnostics *diagnostic.Service,
) (modelsOpenerResult, error) {
	service, err := modelresource.New(
		resolver,
		logs,
		modelresource.Options{
			Logger: diagnostics.Logger("models.resource"),
		},
	)
	if err != nil {
		return modelsOpenerResult{}, err
	}

	return modelsOpenerResult{Opener: service}, nil
}

func newProductionResolver(
	paths config.Paths,
	builder *resource.Builder,
	providerRegistry *providers.Registry,
	diagnostics *diagnostic.Service,
) (*providergateway.Resolver, error) {
	discovery, err := providergateway.NewDiscovery(providerRegistry)
	if err != nil {
		return nil, err
	}

	factory := func(
		ctx context.Context,
		progress providergateway.ProgressReporter,
	) (*providergateway.Gateway, error) {
		operation := progressio.Start(
			progress,
			providergateway.ResourceKind,
			"Opening models gateway endpoints",
		)
		runtimePaths, err := paths.ResolveRuntime()
		if err != nil {
			operation.Fail("Opening models gateway endpoints failed")
			return nil, err
		}
		authPath := filepath.Join(
			runtimePaths.Caddy,
			"auth.sock",
		)

		var gateway *providergateway.Gateway
		pool, err := caddy.NewPool(
			paths,
			builder,
			caddy.DefaultImage,
			authPath,
			func() (*os.File, error) {
				if gateway == nil {
					return nil, fmt.Errorf(
						"models authorization capability is unavailable",
					)
				}

				return gateway.AuthorizationFile()
			},
			caddy.PoolOptions{},
			diagnostics,
		)
		if err != nil {
			operation.Fail("Opening models gateway endpoints failed")
			return nil, err
		}

		gateway, err = providergateway.NewGateway(
			ctx,
			authPath,
			pool,
			discovery,
			providergateway.Options{
				Logger: diagnostics.Logger("provider.gateway"),
			},
		)
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				15*time.Second,
			)
			defer cancel()
			diagnostics.Logger("provider.gateway").DebugError(
				"shut down Caddy pool after models gateway setup failed",
				pool.Shutdown(cleanupCtx),
			)
			operation.Fail("Opening models gateway endpoints failed")
			return nil, err
		}
		operation.Complete("Models gateway endpoints open")

		return gateway, nil
	}

	resolver, err := providergateway.NewResolver(
		factory,
		providergateway.Options{},
	)
	if err != nil {
		return nil, err
	}

	return resolver, nil
}
