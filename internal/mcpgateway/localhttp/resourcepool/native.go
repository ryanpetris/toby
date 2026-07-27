package resourcepool

// Resolves immutable OCI identities and starts ready UDS-backed local HTTP MCP
// generations through the shared sidecar provider.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localhttp"
	"petris.dev/toby/internal/mcpgateway/sidecar"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
)

const (
	nativeBridgeVersion   = "1"
	nativeProtocolVersion = "streamable-http"
	defaultReadyPoll      = 20 * time.Millisecond
)

// Native implements both immutable process planning and concrete Bubblewrap
// generation startup.
type Native struct {
	sidecars    sidecar.Provider
	readyPoll   time.Duration
	dialTimeout time.Duration
	logger      *diagnostic.Logger
}

var _ Planner = (*Native)(nil)
var _ Starter = (*Native)(nil)

// NewNative constructs the local HTTP process implementation.
func NewNative(
	sidecars sidecar.Provider,
	readyPoll time.Duration,
	logger *diagnostic.Logger,
) (*Native, error) {
	if isNilContract(sidecars) {
		return nil, fmt.Errorf("local HTTP MCP sidecar provider is required")
	}
	if readyPoll == 0 {
		readyPoll = defaultReadyPoll
	}
	if readyPoll < 0 {
		return nil, fmt.Errorf(
			"local HTTP MCP readiness interval must not be negative",
		)
	}

	return &Native{
		sidecars:    sidecars,
		readyPoll:   readyPoll,
		dialTimeout: time.Second,
		logger:      logger,
	}, nil
}

// Plan resolves the mutable image reference once, then replaces it with its
// immutable digest reference in the retained launch definition.
func (n *Native) Plan(
	ctx context.Context,
	definition localhttp.Definition,
	progress mcpgateway.ProgressReporter,
) (result Plan, returnErr error) {
	if n == nil || n.sidecars == nil {
		return Plan{}, fmt.Errorf(
			"local HTTP MCP native planner is not configured",
		)
	}
	if err := validateNativeDefinition(definition); err != nil {
		return Plan{}, err
	}

	capabilities, err := n.sidecars.PinMounts(
		ctx,
		definition.Mounts,
	)
	if err != nil {
		return Plan{}, fmt.Errorf(
			"pin local HTTP MCP mounts: %w",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			n.logger.DebugError(
				"close local HTTP MCP mount capabilities after planning failure",
				capabilities.Close(),
			)
		}
	}()

	metadata, err := n.sidecars.Resolve(
		ctx,
		sidecarDefinition(definition),
		progress,
	)
	if err != nil {
		return Plan{}, fmt.Errorf(
			"resolve local HTTP MCP image: %w",
			err,
		)
	}
	definition.Image = metadata.ImmutableImage

	mountIdentities, err := capabilities.Identities()
	if err != nil {
		return Plan{}, fmt.Errorf(
			"identify local HTTP MCP mounts: %w",
			err,
		)
	}

	environment := make(
		[]resource.EnvironmentVariable,
		0,
		len(definition.Environment),
	)
	names := make([]string, 0, len(definition.Environment))
	for name := range definition.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		environment = append(environment, resource.EnvironmentVariable{
			Name:      name,
			Value:     definition.Environment[name],
			Sensitive: true,
		})
	}
	mounts := make([]resource.Mount, len(definition.Mounts))
	for index, item := range definition.Mounts {
		mounts[index] = resource.Mount{
			Source:         item.Source,
			SourceIdentity: mountIdentities[item.Target],
			Target:         item.Target,
			Access:         string(item.Access),
			Scope:          item.Scope,
		}
	}

	return Plan{
		Resource: resource.Spec{
			Kind:           resource.KindMCPHTTP,
			Transport:      resource.TransportHTTP,
			ManifestDigest: metadata.ManifestDigest,
			RootFSDigest:   metadata.RootFSDigest,
			Argv: append(
				[]string(nil),
				definition.Command...,
			),
			Workdir: metadata.Workdir,
			Identity: resource.Identity{
				UID: os.Geteuid(),
				GID: os.Getegid(),
			},
			Environment: environment,
			Endpoint: resource.Endpoint{
				Kind:   resource.EndpointUnix,
				Socket: definition.Endpoint.Socket,
				Path:   definition.Endpoint.Path,
			},
			Mounts:          mounts,
			Network:         definition.Network,
			IdleTimeout:     definition.IdleTimeout.Duration,
			BridgeVersion:   nativeBridgeVersion,
			ProtocolVersion: nativeProtocolVersion,
			RequestedScope:  definition.Scope,
			RunAuthority:    resource.RunAuthorityAbsent,
			ScopeIdentity:   definition.ScopeIdentity,
		},
		Definition:   definition,
		Capabilities: capabilities,
	}, nil
}

// Start launches one generation and returns only after its private Unix socket
// completes an MCP initialize/initialized exchange.
func (n *Native) Start(
	ctx context.Context,
	plan Plan,
	_ uint64,
) (Instance, error) {
	if n == nil || n.sidecars == nil {
		return nil, fmt.Errorf(
			"local HTTP MCP native starter is not configured",
		)
	}
	if err := validatePlan(plan); err != nil {
		return nil, err
	}
	if err := validateNativeDefinition(plan.Definition); err != nil {
		return nil, err
	}

	if plan.Capabilities == nil {
		return nil, fmt.Errorf(
			"local HTTP MCP mount capabilities are unavailable",
		)
	}
	prepared, err := n.sidecars.PreparePinned(
		ctx,
		sidecarDefinition(plan.Definition),
		plan.Capabilities,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"prepare local HTTP MCP sidecar: %w",
			err,
		)
	}
	metadata := prepared.Metadata()
	if metadata.ImmutableImage != plan.Definition.Image ||
		metadata.ManifestDigest != plan.Resource.ManifestDigest ||
		metadata.RootFSDigest != plan.Resource.RootFSDigest ||
		metadata.Workdir != plan.Resource.Workdir {
		n.logger.DebugError(
			"close mismatched local HTTP MCP sidecar",
			prepared.Close(),
		)
		return nil, fmt.Errorf(
			"local HTTP MCP immutable image identity changed",
		)
	}

	process, err := prepared.Start(ctx, bwrap.ProcessIO{}, true)
	if err != nil {
		n.logger.DebugError(
			"close local HTTP MCP sidecar after startup failure",
			prepared.Close(),
		)
		return nil, fmt.Errorf(
			"start local HTTP MCP sidecar: %w",
			err,
		)
	}
	hostSocket := filepath.Join(
		process.RuntimePath(),
		path.Base(plan.Definition.Endpoint.Socket),
	)
	instance := newNativeInstance(
		process,
		hostSocket,
		plan.Definition.Endpoint.Path,
	)
	if err := n.waitReady(ctx, process, hostSocket, instance); err != nil {
		return instance, err
	}

	return instance, nil
}

func (n *Native) waitReady(
	ctx context.Context,
	process *sidecar.Process,
	hostSocket string,
	instance *nativeInstance,
) error {
	timer := time.NewTimer(0)
	defer timer.Stop()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return errors.Join(
				fmt.Errorf(
					"local HTTP MCP readiness was canceled: %w",
					context.Cause(ctx),
				),
				lastErr,
			)
		case <-process.Done():
			return errors.Join(
				fmt.Errorf(
					"local HTTP MCP exited before readiness",
				),
				process.Err(),
				lastErr,
			)
		case <-timer.C:
		}

		if readyUnixSocket(hostSocket) {
			retryable, err := n.probeMCP(ctx, instance)
			if err == nil {
				return nil
			}
			if !retryable {
				return err
			}
			lastErr = err
		}
		timer.Reset(n.readyPoll)
	}
}

func (n *Native) probeMCP(
	ctx context.Context,
	instance *nativeInstance,
) (bool, error) {
	upstream, err := instance.Upstream()
	if err != nil {
		return true, err
	}

	probeClient := *upstream.HTTPClient
	probeClient.Timeout = n.dialTimeout
	baseTransport := probeClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	var responseMu sync.Mutex
	sawResponse := false
	onlyTransientResponses := true
	probeClient.Transport = readinessRoundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		response, roundTripErr := baseTransport.RoundTrip(request)
		if response != nil {
			responseMu.Lock()
			sawResponse = true
			if !transientReadinessStatus(response.StatusCode) {
				onlyTransientResponses = false
			}
			responseMu.Unlock()
		}

		return response, roundTripErr
	})
	probeCtx, cancel := context.WithTimeout(ctx, n.dialTimeout)
	defer cancel()

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "toby-readiness",
			Version: nativeBridgeVersion,
		},
		nil,
	)
	session, err := client.Connect(
		probeCtx,
		&mcp.StreamableClientTransport{
			Endpoint:   upstream.Endpoint,
			HTTPClient: &probeClient,
			MaxRetries: -1,
		},
		nil,
	)
	if err != nil {
		responseMu.Lock()
		retryable := !sawResponse || onlyTransientResponses
		responseMu.Unlock()
		return retryable, fmt.Errorf(
			"initialize local HTTP MCP endpoint: %w",
			err,
		)
	}
	n.logger.DebugError(
		"close local HTTP MCP readiness session",
		session.Close(),
	)

	return false, nil
}

type readinessRoundTripFunc func(
	*http.Request,
) (*http.Response, error)

var _ http.RoundTripper = readinessRoundTripFunc(nil)

func (f readinessRoundTripFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return f(request)
}

func transientReadinessStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func validateNativeDefinition(definition localhttp.Definition) error {
	if definition.Endpoint.Kind != mcpgateway.EndpointUnix {
		return fmt.Errorf(
			"local HTTP MCP sidecars currently require a Unix endpoint",
		)
	}
	if path.Dir(definition.Endpoint.Socket) != layout.Runtime ||
		path.Base(definition.Endpoint.Socket) == "." {
		return fmt.Errorf(
			"local HTTP MCP Unix socket must be directly beneath %s",
			layout.Runtime,
		)
	}

	return nil
}

func sidecarDefinition(
	definition localhttp.Definition,
) sidecar.Definition {
	return sidecar.Definition{
		Image:       definition.Image,
		Command:     append([]string(nil), definition.Command...),
		Environment: cloneStringMap(definition.Environment),
		Mounts: append(
			[]mcpgateway.Mount(nil),
			definition.Mounts...,
		),
		Network: definition.Network,
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}

	return result
}

func readyUnixSocket(name string) bool {
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	return true
}
