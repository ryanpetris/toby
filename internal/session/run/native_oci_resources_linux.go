//go:build linux

package run

// Prepares every launch-required OCI image through independently leased
// agent resources and replays each operation into launch status.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	appconfig "petris.dev/toby/internal/config/app"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/oci/prepareclient"
	"petris.dev/toby/internal/providergateway/caddy"
	"petris.dev/toby/internal/status"
)

type namedOCIResource struct {
	configuration ociresource.Config
	display       string
	kind          ociResourceKind
	mcpNames      []string
}

type ociResourceKind int

const (
	ociResourceSandbox ociResourceKind = iota
	ociResourceCaddy
	ociResourceMCP
)

type ociPrepareResult struct {
	request namedOCIResource
	err     error
}

func (r *NativeRunner) prepareNativeOCIResources(
	ctx context.Context,
	session *agentclient.AgentSession,
	sandbox appconfig.SandboxConfig,
	resources *appconfig.ResourcesConfig,
	profile string,
	project string,
) (sandboxResource ociresource.Config, returnErr error) {
	registry, err := clientresource.NewRegistry(
		protocol.ResourceOCI,
		session,
		r.logger.With("resource_kind", protocol.ResourceOCI),
	)
	if err != nil {
		return ociresource.Config{}, err
	}
	defer func() {
		closeCtx, cancel := r.shutdown.CleanupContext()
		defer cancel()
		r.logger.DebugError(
			"release OCI resources",
			registry.Close(closeCtx),
		)
	}()

	requests, sandboxResource, err := nativeOCIResourceRequests(
		sandbox,
		*resources,
		profile,
		project,
	)
	if err != nil {
		return ociresource.Config{}, err
	}

	results := make(chan ociPrepareResult, len(requests))
	var workers sync.WaitGroup
	for _, request := range requests {
		clientID, err := registry.Acquire(
			ctx,
			request.configuration,
		)
		if err != nil {
			results <- ociPrepareResult{request: request, err: err}
			continue
		}

		workers.Add(1)
		go func(
			request namedOCIResource,
			clientID protocol.ClientResourceID,
		) {
			defer workers.Done()
			err := r.prepareNativeOCIResource(
				ctx,
				registry,
				clientID,
				request.display,
			)
			results <- ociPrepareResult{request: request, err: err}
		}(request, clientID)
	}

	workers.Wait()
	close(results)

	for result := range results {
		if err := applyUnavailableOCIResource(
			r.warnings,
			resources,
			result,
		); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}

	return sandboxResource, returnErr
}

func applyUnavailableOCIResource(
	warnings *warning.Service,
	resources *appconfig.ResourcesConfig,
	result ociPrepareResult,
) error {
	if result.err == nil {
		return nil
	}

	switch result.request.kind {
	case ociResourceSandbox:
		return fmt.Errorf(
			"register OCI image %q: %w",
			result.request.configuration.Reference,
			result.err,
		)
	case ociResourceCaddy:
		if warnings != nil {
			warnings.WarnError(
				warning.ModelsEndpointUnavailable,
				"models gateway image is unavailable; skipping models endpoints",
				result.err,
				"image", result.request.configuration.Reference,
			)
		}
		resources.Models = make(map[string]modelsconfig.Config)
		return nil
	case ociResourceMCP:
		if warnings != nil {
			warnings.WarnError(
				warning.MCPImageUnavailable,
				fmt.Sprintf(
					"MCP sidecar image %q is unavailable; skipping dependent servers",
					result.request.configuration.Reference,
				),
				result.err,
				"image", result.request.configuration.Reference,
			)
		}
		for _, name := range result.request.mcpNames {
			delete(resources.MCPs, name)
		}
		return nil
	default:
		return fmt.Errorf(
			"register OCI image %q: %w",
			result.request.configuration.Reference,
			result.err,
		)
	}
}

func nativeOCIResourceRequests(
	sandbox appconfig.SandboxConfig,
	resources appconfig.ResourcesConfig,
	profile string,
	project string,
) ([]namedOCIResource, ociresource.Config, error) {
	type pendingRequest struct {
		input    ociresource.Config
		kind     ociResourceKind
		mcpNames []string
	}
	sandboxRequest := ociresource.Config{
		Source:    sandbox.Source,
		Reference: sandbox.Image,
		Archive:   sandbox.Archive,
		Build:     sandbox.Build,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: sandbox.Pull,
	}
	if sandboxRequest.Source == imagesource.Build {
		sandboxRequest.Profile = profile
		sandboxRequest.Project = project
	}
	pending := []pendingRequest{{
		input: sandboxRequest,
		kind:  ociResourceSandbox,
	}}

	names := make([]string, 0, len(resources.MCPs))
	for name := range resources.MCPs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := resources.MCPs[name]
		if server.Type != mcpconfig.ServerLocal {
			continue
		}
		pending = append(pending, pendingRequest{
			input: ociresource.Config{
				Reference: server.Image,
				Source:    imagesource.Registry,
				Platform: ocispec.Platform{
					OS:           "linux",
					Architecture: runtime.GOARCH,
				},
				PullPolicy: image.PullIfMissing,
			},
			kind:     ociResourceMCP,
			mcpNames: []string{name},
		})
	}
	if len(resources.Models) != 0 {
		pending = append(pending, pendingRequest{
			input: ociresource.Config{
				Reference: caddy.DefaultImage,
				Source:    imagesource.Registry,
				Platform: ocispec.Platform{
					OS:           "linux",
					Architecture: runtime.GOARCH,
				},
				PullPolicy: image.PullIfMissing,
			},
			kind: ociResourceCaddy,
		})
	}

	result := make([]namedOCIResource, 0, len(pending))
	indexByKey := make(map[string]int, len(pending))
	var sandboxResource ociresource.Config
	for _, item := range pending {
		effective, err := ociresource.Normalize(item.input)
		if err != nil {
			return nil, ociresource.Config{}, err
		}
		if sandboxResource.Reference == "" {
			sandboxResource = effective
		}
		encoded, err := json.Marshal(effective)
		if err != nil {
			return nil, ociresource.Config{}, fmt.Errorf(
				"encode OCI resource identity: %w",
				err,
			)
		}
		key := string(encoded)
		if existing, duplicate := indexByKey[key]; duplicate {
			result[existing].mcpNames = append(
				result[existing].mcpNames,
				item.mcpNames...,
			)
			continue
		}
		indexByKey[key] = len(result)
		result = append(result, namedOCIResource{
			configuration: effective,
			display:       ociResourceDisplay(effective),
			kind:          item.kind,
			mcpNames:      append([]string(nil), item.mcpNames...),
		})
	}

	return result, sandboxResource, nil
}

func ociResourceDisplay(configuration ociresource.Config) string {
	switch configuration.Source {
	case imagesource.Archive:
		return filepath.Base(configuration.Archive)
	case imagesource.Build:
		return filepath.Base(configuration.Build.Context)
	default:
		return configuration.Reference
	}
}

func (r *NativeRunner) prepareNativeOCIResource(
	ctx context.Context,
	registry *clientresource.Registry,
	clientID protocol.ClientResourceID,
	reference string,
) error {
	return prepareclient.Follow(
		ctx,
		registry,
		clientID,
		reference,
		prepareclient.Presentation{
			Start: func() *status.Operation {
				return r.status.StartOperation(
					"Preparing OCI image " + reference,
				)
			},
			Stdout: r.stdout,
			Stderr: r.stderr,
			Logger: r.logger.With(
				"image",
				reference,
			),
		},
	)
}
