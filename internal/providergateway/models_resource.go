package providergateway

// Adapts one typed models resource to a retained Caddy route and serves
// lease-authorized raw streams without exposing agent route credentials.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	configfile "petris.dev/toby/internal/config/file"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/diagnostic"
)

// ModelsBackend owns one ready agent-private Caddy route.
type ModelsBackend struct {
	acquired   *acquired
	descriptor ProviderDescriptor
	discoverer ModelDiscoverer
	logger     *diagnostic.Logger
}

// AcquireModels lazily initializes the shared gateway and installs one models
// resource route. Dynamic model discovery remains a separate operation.
func (r *Resolver) AcquireModels(
	ctx context.Context,
	configuration modelsconfig.Config,
	progress ProgressReporter,
) (*ModelsBackend, error) {
	if r == nil {
		return nil, fmt.Errorf("models gateway resolver is nil")
	}
	effective, err := modelsconfig.Normalize(configuration)
	if err != nil {
		return nil, err
	}

	providerType := ProviderType(effective.Protocol)
	displayName := effective.Name
	if displayName == "" {
		displayName = "models"
	}
	spec := RequestSpec{Providers: []ProviderSpec{{
		ID:      "resource",
		Type:    providerType,
		Name:    displayName,
		URL:     effective.URL,
		Headers: cloneStringMap(effective.Headers),
	}}}

	gateway, err := r.gatewayFor(ctx, progress)
	if err != nil {
		return nil, err
	}
	ready, err := gateway.acquire(ctx, spec, progress)
	if err != nil {
		return nil, err
	}
	if len(ready.descriptor.Providers) != 1 {
		ready.Revoke()
		r.options.Logger.DebugError(
			"release invalid models gateway route",
			ready.Release(ctx),
		)
		return nil, fmt.Errorf(
			"models gateway did not publish one route",
		)
	}

	return &ModelsBackend{
		acquired:   ready,
		descriptor: ready.descriptor.Providers[0].clone(),
		discoverer: gateway.discoverer,
		logger:     r.options.Logger,
	}, nil
}

// Discover performs one uncached model lookup through the agent-private
// route owned by this backend.
func (b *ModelsBackend) Discover(
	ctx context.Context,
) (map[string]any, error) {
	if b == nil || b.acquired == nil || b.discoverer == nil {
		return nil, fmt.Errorf("models gateway discovery is unavailable")
	}
	if ctx == nil {
		return nil, fmt.Errorf("models discovery context is nil")
	}

	models, err := b.discoverer.Discover(ctx, b.descriptor.clone())
	if err != nil {
		return nil, err
	}
	if err := validateModels(models); err != nil {
		return nil, err
	}

	return configfile.CloneMap(models), nil
}

// Revoke synchronously prevents new authorization through this route.
func (b *ModelsBackend) Revoke() {
	if b == nil || b.acquired == nil {
		return
	}
	b.acquired.Revoke()
}

// Release performs bounded route cleanup.
func (b *ModelsBackend) Release(ctx context.Context) error {
	if b == nil || b.acquired == nil {
		return nil
	}
	return b.acquired.Release(ctx)
}

// Serve proxies one raw HTTP connection through the agent-private route.
func (b *ModelsBackend) Serve(
	ctx context.Context,
	connection net.Conn,
) error {
	if b == nil || b.acquired == nil {
		return fmt.Errorf("models gateway backend is unavailable")
	}
	if ctx == nil {
		return fmt.Errorf("models gateway stream context is nil")
	}
	if connection == nil {
		return fmt.Errorf("models gateway stream connection is nil")
	}

	target, err := url.Parse(b.descriptor.URL)
	if err != nil {
		return fmt.Errorf("parse models gateway route: %w", err)
	}
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(request *httputil.ProxyRequest) {
		request.SetURL(target)
		request.Out.Host = target.Host
		switch b.descriptor.Type {
		case ProviderOpenAI:
			request.Out.Header.Set(
				openAICredentialHeader,
				"Bearer "+b.descriptor.Credential,
			)
			request.Out.Header.Del(anthropicCredentialHeader)
		case ProviderAnthropic:
			request.Out.Header.Set(
				anthropicCredentialHeader,
				b.descriptor.Credential,
			)
			request.Out.Header.Del(openAICredentialHeader)
		}
	}
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
	}
	defer transport.CloseIdleConnections()
	proxy.Transport = transport
	proxy.ErrorHandler = func(
		writer http.ResponseWriter,
		_ *http.Request,
		err error,
	) {
		b.logger.DebugError(
			"proxy models request",
			err,
		)
		writer.WriteHeader(http.StatusBadGateway)
	}

	listener := newSingleConnectionListener(connection)
	server := &http.Server{Handler: proxy}
	var closeOnce sync.Once
	var closeErr error
	closeConnection := func() {
		closeOnce.Do(func() {
			closeErr = connection.Close()
		})
	}
	stop := context.AfterFunc(ctx, closeConnection)

	serveErr := server.Serve(listener)
	stop()
	if ctx.Err() != nil {
		closeConnection()
	}
	if errors.Is(serveErr, net.ErrClosed) ||
		errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}

	b.logger.DebugError(
		"close models gateway stream connection",
		closeErr,
	)
	return serveErr
}
