//go:build linux

package run

// Owns the launch-side loopback models capability and translates local HTTP
// requests into lease-authorized agent resource streams.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	appconfig "petris.dev/toby/internal/config/app"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/shutdown"
)

const (
	nativeModelsReleaseTimeout = shutdown.ClientResourceReleaseGrace
	nativeModelsHeaderTimeout  = 10 * time.Second
	nativeModelsListTimeout    = 2 * time.Minute
	nativeModelsListAttempts   = 3
	nativeModelsListRetryMin   = 100 * time.Millisecond
	nativeModelsIdleTimeout    = 30 * time.Second
)

type nativeModelsResources struct {
	registry  *clientresource.Registry
	listener  *net.TCPListener
	server    *http.Server
	bindings  []*nativeModelsBinding
	openers   map[string]sandboxgateway.Opener
	endpoints []sessionconfig.ModelsEndpoint
	done      chan struct{}
	logger    *diagnostic.Logger
}

type nativeModelsBinding struct {
	id         protocol.ClientResourceID
	protocol   modelsconfig.Protocol
	credential string
	transport  *http.Transport
	logger     *diagnostic.Logger
}

var _ http.Handler = (*nativeModelsBinding)(nil)

func acquireNativeModelsResources(
	ctx context.Context,
	session *agentclient.AgentSession,
	config *appconfig.Service,
	models map[string]modelsconfig.Config,
	logger *diagnostic.Logger,
	warnings *warning.Service,
	cleanupContext func() context.Context,
) (result *nativeModelsResources, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"acquire native models resources: context is nil",
		)
	}
	if session == nil {
		return nil, fmt.Errorf(
			"acquire native models resources: agent session is required",
		)
	}

	registry, err := clientresource.NewRegistry(
		protocol.ResourceModels,
		session,
		logger.With("resource_kind", protocol.ResourceModels),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			logger.DebugError(
				"close models resource registry after acquisition failure",
				closeNativeModelsRegistry(registry, cleanupContext),
			)
		}
	}()

	listener, err := net.ListenTCP(
		"tcp4",
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"listen on launch models capability: %w",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			logger.DebugError(
				"close models capability listener after acquisition failure",
				listener.Close(),
			)
		}
	}()

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port <= 0 {
		return nil, fmt.Errorf(
			"launch models capability has an invalid address",
		)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", address.Port)
	mux := http.NewServeMux()
	bindings := make([]*nativeModelsBinding, 0, len(models))
	openers := make(
		map[string]sandboxgateway.Opener,
		len(models),
	)
	endpoints := make([]sessionconfig.ModelsEndpoint, 0, len(models))

	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		effective, err := resolveNativeModelsConfiguration(
			config,
			name,
			models[name],
		)
		if err != nil {
			return nil, err
		}
		clientID, err := registry.Acquire(ctx, effective)
		if err != nil {
			warnModelsEndpoint(
				warnings,
				name,
				err,
			)
			continue
		}
		items, err := listNativeModels(
			ctx,
			registry,
			clientID,
		)
		if err != nil {
			warnModelsEndpoint(
				warnings,
				name,
				err,
			)
			continue
		}
		modelDocument, err := nativeModelsDocument(items)
		if err != nil {
			warnModelsEndpoint(
				warnings,
				name,
				err,
			)
			continue
		}
		credential, err := newNativeModelsCredential()
		if err != nil {
			return nil, err
		}

		binding := newNativeModelsBinding(
			registry,
			clientID,
			effective.Protocol,
			credential,
			logger,
		)
		bindings = append(bindings, binding)
		openers[string(clientID)] = sandboxgateway.OpenFunc(
			func(
				openContext context.Context,
			) (io.ReadWriteCloser, error) {
				return registry.Open(openContext, clientID)
			},
		)
		prefix := "/" + string(clientID)
		mux.Handle(
			prefix+"/",
			http.StripPrefix(prefix, binding),
		)

		displayName := effective.Name
		if displayName == "" {
			displayName = name
		}
		endpoints = append(endpoints, sessionconfig.ModelsEndpoint{
			ID:         name,
			Type:       string(effective.Protocol),
			Name:       displayName,
			URL:        baseURL + prefix,
			Credential: credential,
			Models:     modelDocument,
		})
	}

	if len(endpoints) == 0 {
		logger.DebugError(
			"close unused models capability listener",
			listener.Close(),
		)
		logger.DebugError(
			"close unused models resource registry",
			closeNativeModelsRegistry(registry, cleanupContext),
		)
		return nil, nil
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: nativeModelsHeaderTimeout,
		IdleTimeout:       nativeModelsIdleTimeout,
		ErrorLog:          logger.StandardLogger(slog.LevelDebug),
	}
	resources := &nativeModelsResources{
		registry:  registry,
		listener:  listener,
		server:    server,
		bindings:  bindings,
		openers:   openers,
		endpoints: endpoints,
		done:      make(chan struct{}),
		logger:    logger,
	}
	go resources.serve()

	return resources, nil
}

func listNativeModels(
	ctx context.Context,
	registry *clientresource.Registry,
	clientID protocol.ClientResourceID,
) ([]protocol.ModelsListItemResponse, error) {
	listCtx, cancel := context.WithTimeout(ctx, nativeModelsListTimeout)
	defer cancel()

	delay := nativeModelsListRetryMin
	var lastErr error
	for attempt := 1; attempt <= nativeModelsListAttempts; attempt++ {
		items, err := registry.ListModels(listCtx, clientID)
		if err == nil {
			return items, nil
		}
		lastErr = err
		if !retryableModelsListError(err) ||
			attempt == nativeModelsListAttempts {
			return nil, err
		}

		timer := time.NewTimer(delay)
		select {
		case <-listCtx.Done():
			timer.Stop()
			return nil, listCtx.Err()
		case <-timer.C:
		}
		delay *= 2
	}

	return nil, lastErr
}

func retryableModelsListError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) {
		return true
	}
	var remote agentclient.RemoteError
	if errors.As(err, &remote) {
		return remote.Retryable
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if st, ok := status.FromError(current); ok {
			switch st.Code() {
			case codes.Unavailable,
				codes.DeadlineExceeded,
				codes.ResourceExhausted:
				return true
			}
		}
	}
	return false
}

func warnModelsEndpoint(
	warnings *warning.Service,
	name string,
	err error,
) {
	if warnings == nil || err == nil {
		return
	}
	warnings.WarnError(
		warning.ModelsEndpointUnavailable,
		fmt.Sprintf(
			"models endpoint %q is unavailable; skipping it",
			name,
		),
		err,
		"models_endpoint", name,
	)
}

func nativeModelsDocument(
	items []protocol.ModelsListItemResponse,
) (map[string]any, error) {
	result := make(map[string]any, len(items))
	for _, item := range items {
		if _, duplicate := result[item.ModelID]; duplicate {
			return nil, fmt.Errorf(
				"model ID %q appears more than once",
				item.ModelID,
			)
		}

		decoder := json.NewDecoder(bytes.NewReader(item.Model))
		decoder.UseNumber()
		var model any
		if err := decoder.Decode(&model); err != nil {
			return nil, fmt.Errorf(
				"decode model %q metadata: %w",
				item.ModelID,
				err,
			)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, fmt.Errorf(
					"model %q metadata has a trailing value",
					item.ModelID,
				)
			}
			return nil, fmt.Errorf(
				"decode model %q trailing metadata: %w",
				item.ModelID,
				err,
			)
		}
		result[item.ModelID] = model
	}

	return result, nil
}

func resolveNativeModelsConfiguration(
	config *appconfig.Service,
	name string,
	model modelsconfig.Config,
) (modelsconfig.Config, error) {
	headers, err := config.ResolveModelHeaders(name, model)
	if err != nil {
		return modelsconfig.Config{}, err
	}

	effective := model.Clone()
	effective.Headers = make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) != 1 {
			return modelsconfig.Config{}, fmt.Errorf(
				"models resource header %q has %d values",
				name,
				len(values),
			)
		}
		effective.Headers[name] = values[0]
	}
	if len(effective.Headers) == 0 {
		effective.Headers = nil
	}

	return modelsconfig.Normalize(effective)
}

func newNativeModelsBinding(
	registry *clientresource.Registry,
	id protocol.ClientResourceID,
	modelProtocol modelsconfig.Protocol,
	credential string,
	logger *diagnostic.Logger,
) *nativeModelsBinding {
	binding := &nativeModelsBinding{
		id:         id,
		protocol:   modelProtocol,
		credential: credential,
		logger:     logger,
	}
	binding.transport = &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		MaxConnsPerHost:       32,
		IdleConnTimeout:       nativeModelsIdleTimeout,
		ResponseHeaderTimeout: 0,
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			return registry.Open(ctx, id)
		},
	}

	return binding
}

func (b *nativeModelsBinding) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if !b.authorized(request.Header) {
		writer.Header().Set("Content-Length", "0")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}

	target := &url.URL{Scheme: "http", Host: "models.resource"}
	proxy := &httputil.ReverseProxy{}
	proxy.Rewrite = func(forward *httputil.ProxyRequest) {
		forward.SetURL(target)
		forward.Out.Host = target.Host
		forward.Out.Header.Del("Authorization")
		forward.Out.Header.Del("X-Api-Key")
	}
	proxy.Transport = b.transport
	proxy.ErrorLog = b.logger.StandardLogger(slog.LevelDebug)
	proxy.ErrorHandler = func(
		response http.ResponseWriter,
		_ *http.Request,
		err error,
	) {
		b.logger.DebugError("proxy models capability request", err)
		response.WriteHeader(http.StatusBadGateway)
	}
	proxy.ServeHTTP(writer, request)
}

func (b *nativeModelsBinding) authorized(headers http.Header) bool {
	var actual string
	switch b.protocol {
	case modelsconfig.ProtocolOpenAI:
		values := headers.Values("Authorization")
		if len(values) == 1 &&
			strings.HasPrefix(values[0], "Bearer ") {
			actual = strings.TrimPrefix(values[0], "Bearer ")
		}
	case modelsconfig.ProtocolAnthropic:
		values := headers.Values("X-Api-Key")
		if len(values) == 1 {
			actual = values[0]
		}
	}
	if len(actual) != len(b.credential) {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(actual),
		[]byte(b.credential),
	) == 1
}

func (r *nativeModelsResources) serve() {
	defer close(r.done)
	err := r.server.Serve(r.listener)
	if !errors.Is(err, http.ErrServerClosed) &&
		!errors.Is(err, net.ErrClosed) {
		r.logger.ErrorError("serve models capability", err)
		closeErr := r.listener.Close()
		if !errors.Is(closeErr, net.ErrClosed) {
			r.logger.DebugError(
				"close models capability listener after serve failure",
				closeErr,
			)
		}
	}
}

func (r *nativeModelsResources) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var result error
	if r.server != nil {
		result = errors.Join(result, r.server.Shutdown(ctx))
	}
	if r.listener != nil {
		closeErr := r.listener.Close()
		if !errors.Is(closeErr, net.ErrClosed) {
			result = errors.Join(result, closeErr)
		}
	}
	if r.done != nil {
		select {
		case <-ctx.Done():
			result = errors.Join(result, ctx.Err())
		case <-r.done:
		}
	}
	for _, binding := range r.bindings {
		binding.transport.CloseIdleConnections()
	}
	r.bindings = nil
	r.openers = nil
	if r.registry != nil {
		result = errors.Join(result, r.registry.Close(ctx))
		r.registry = nil
	}

	return result
}

func closeNativeModelsRegistry(
	registry *clientresource.Registry,
	cleanupContext func() context.Context,
) error {
	if registry == nil {
		return nil
	}
	if cleanupContext != nil {
		return registry.Close(cleanupContext())
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		nativeModelsReleaseTimeout,
	)
	defer cancel()

	return registry.Close(ctx)
}

func newNativeModelsCredential() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate models capability credential: %w", err)
	}

	return hex.EncodeToString(value[:]), nil
}
