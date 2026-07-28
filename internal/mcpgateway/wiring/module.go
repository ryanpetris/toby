package wiring

// Provides the shared MCP bridge, static backend resolvers, resource service,
// and process-final sidecar cleanup through Fx.

import (
	"context"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/builtin"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/mcpgateway/localhttp"
	"petris.dev/toby/internal/mcpgateway/localhttp/resourcepool"
	"petris.dev/toby/internal/mcpgateway/localstdio"
	"petris.dev/toby/internal/mcpgateway/remotehttp"
	"petris.dev/toby/internal/mcpgateway/resourcehandler"
	"petris.dev/toby/internal/mcpgateway/sidecar"
	"petris.dev/toby/internal/sandbox/pasta"
	"petris.dev/toby/internal/tobymcp"
	gitservice "petris.dev/toby/internal/tobymcp/services/git"
	sessionservice "petris.dev/toby/internal/tobymcp/services/session"

	"go.uber.org/fx"
)

// Module provides the process-wide MCP gateway services. Per-run gateways,
// connectors, and sidecar process generations remain ordinary lifecycle-owned
// objects outside the Fx graph.
func Module() fx.Option {
	return fx.Module(
		"mcpgateway",
		tobymcp.Module(),
		gitservice.Module(),
		sessionservice.Module(),
		pasta.Module(),
		fx.Provide(
			newHTTPBridge,
			sidecar.NewNativeLazy,
			newStdioLauncher,
			newLocalStdioResolver,
			newNativeHTTP,
			newLocalHTTPPool,
			newLocalHTTPResolver,
			newRemoteHTTPResolver,
			newBuiltinResolver,
			newResourceService,
		),
		fx.Invoke(registerCleanup),
	)
}

func newHTTPBridge(
	diagnostics *diagnostic.Service,
) (*httpbridge.Bridge, error) {
	return httpbridge.New(httpbridge.Options{
		Logger: diagnostics.Logger("mcp.http-bridge"),
	})
}

func newStdioLauncher(
	sidecars *sidecar.Lazy,
	diagnostics *diagnostic.Service,
) (*sidecar.StdioLauncher, error) {
	return sidecar.NewStdioLauncher(
		sidecars,
		0,
		diagnostics.Logger("mcp.sidecar.stdio"),
	)
}

func newLocalStdioResolver(
	launcher *sidecar.StdioLauncher,
	diagnostics *diagnostic.Service,
) (*localstdio.Resolver, error) {
	return localstdio.NewResolver(
		launcher,
		diagnostics.Logger("mcp.local-stdio"),
	)
}

func newNativeHTTP(
	sidecars *sidecar.Lazy,
	diagnostics *diagnostic.Service,
) (*resourcepool.Native, error) {
	return resourcepool.NewNative(
		sidecars,
		0,
		diagnostics.Logger("mcp.local-http-pool"),
	)
}

func newLocalHTTPPool(
	builder *resource.Builder,
	native *resourcepool.Native,
	diagnostics *diagnostic.Service,
) (*resourcepool.Pool, error) {
	return resourcepool.New(
		builder,
		native,
		native,
		resource.Options{},
		diagnostics.Logger("mcp.local-http-pool"),
	)
}

func newLocalHTTPResolver(
	pool *resourcepool.Pool,
	bridge *httpbridge.Bridge,
	diagnostics *diagnostic.Service,
) (*localhttp.Resolver, error) {
	return localhttp.NewResolver(
		pool,
		bridge,
		diagnostics.Logger("mcp.local-http"),
	)
}

func newRemoteHTTPResolver(
	bridge *httpbridge.Bridge,
	diagnostics *diagnostic.Service,
) (*remotehttp.Resolver, error) {
	return remotehttp.NewResolver(
		bridge,
		diagnostics.Logger("mcp.remote-http"),
	)
}

func newBuiltinResolver(
	runner *tobymcp.Runner,
	diagnostics *diagnostic.Service,
) (*builtin.Resolver, error) {
	return builtin.NewResolver(
		runner,
		tobymcp.DecodeSessionSnapshot,
		diagnostics.Logger("mcp.builtin"),
	)
}

type resourceOpenerResult struct {
	fx.Out

	Opener resourcelease.ResourceOpener `group:"resourceOpeners"`
}

func newResourceService(
	builtinResolver *builtin.Resolver,
	localHTTPResolver *localhttp.Resolver,
	localStdioResolver *localstdio.Resolver,
	remoteHTTPResolver *remotehttp.Resolver,
	logs *resourcelog.Service,
	diagnostics *diagnostic.Service,
) (resourceOpenerResult, error) {
	service, err := resourcehandler.New([]mcpgateway.BackendResolver{
		builtinResolver,
		localHTTPResolver,
		localStdioResolver,
		remoteHTTPResolver,
	}, logs, resourcehandler.Options{
		Logger: diagnostics.Logger("mcp.resource"),
	})
	if err != nil {
		return resourceOpenerResult{}, err
	}

	return resourceOpenerResult{Opener: service}, nil
}

func registerCleanup(
	lifecycle fx.Lifecycle,
	sidecars *sidecar.Lazy,
) {
	lifecycle.Append(fx.Hook{
		OnStop: func(context.Context) error {
			return sidecars.Close()
		},
	})
}
