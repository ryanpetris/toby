//go:build linux

package run

// Prepares every launch-required OCI image through independently leased
// agent resources and replays each operation into launch status.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	appconfig "petris.dev/toby/internal/config/app"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/prepareclient"
	"petris.dev/toby/internal/providergateway/caddy"
	"petris.dev/toby/internal/status"
)

type namedOCIResource struct {
	configuration ociresource.Config
}

func (r *NativeRunner) prepareNativeOCIResources(
	ctx context.Context,
	session *agentclient.AgentSession,
	sandbox appconfig.SandboxConfig,
	resources appconfig.ResourcesConfig,
) (returnErr error) {
	registry, err := clientresource.NewRegistry(
		protocol.ResourceOCI,
		session,
		r.logger.With("resource_kind", protocol.ResourceOCI),
	)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := r.shutdown.CleanupContext()
		defer cancel()
		r.logger.DebugError(
			"release OCI resources",
			registry.Close(closeCtx),
		)
	}()

	requests, err := nativeOCIResourceRequests(sandbox, resources)
	if err != nil {
		return err
	}

	results := make(chan error, len(requests))
	var workers sync.WaitGroup
	for _, request := range requests {
		clientID, err := registry.Acquire(
			ctx,
			request.configuration,
		)
		if err != nil {
			workers.Wait()
			return fmt.Errorf(
				"register OCI image %q: %w",
				request.configuration.Reference,
				err,
			)
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
				request.configuration.Reference,
			)
			results <- err
		}(request, clientID)
	}

	workers.Wait()
	close(results)

	for err := range results {
		returnErr = errors.Join(returnErr, err)
	}

	return returnErr
}

func nativeOCIResourceRequests(
	sandbox appconfig.SandboxConfig,
	resources appconfig.ResourcesConfig,
) ([]namedOCIResource, error) {
	requested := []ociresource.Config{{
		Reference: sandbox.Image,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: sandbox.Pull,
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
		requested = append(requested, ociresource.Config{
			Reference: server.Image,
			Platform: ocispec.Platform{
				OS:           "linux",
				Architecture: runtime.GOARCH,
			},
			PullPolicy: image.PullIfMissing,
		})
	}
	if len(resources.Models) != 0 {
		requested = append(requested, ociresource.Config{
			Reference: caddy.DefaultImage,
			Platform: ocispec.Platform{
				OS:           "linux",
				Architecture: runtime.GOARCH,
			},
			PullPolicy: image.PullIfMissing,
		})
	}

	result := make([]namedOCIResource, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, input := range requested {
		effective, err := ociresource.Normalize(input)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(effective)
		if err != nil {
			return nil, fmt.Errorf(
				"encode OCI resource identity: %w",
				err,
			)
		}
		key := string(encoded)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, namedOCIResource{
			configuration: effective,
		})
	}

	return result, nil
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
				operation := r.status.StartOperation(
					"Preparing OCI image " + reference,
				)
				operation.SetProgress(status.Progress{
					OCIAction:    "Preparing",
					OCIReference: reference,
				})
				return operation
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
