package cli

// Pulls OCI images through agent-owned preparation resources while reusing
// the launch progress presentation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"

	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/prepareclient"
	"petris.dev/toby/internal/status"
)

const imagePullReleaseTimeout = 15 * time.Second

type imagePullBinding struct {
	config   ociresource.Config
	clientID protocol.ClientResourceID
}

func newImagePullCommand(
	params Params,
	rootFlags *rootFlagValues,
) *cobra.Command {
	platformValue := ""
	command := &cobra.Command{
		Use:   imagePullCommandName + " <reference>...",
		Short: "Pull and extract OCI images into the per-user store.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platform, err := parseImagePlatform(platformValue, true)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			debug := false
			quiet := false
			if rootFlags != nil {
				debug = rootFlags.debug
				quiet = rootFlags.quiet
			}
			if debug && quiet {
				return exitcode.New(
					2,
					"--debug and --quiet are mutually exclusive",
				)
			}
			return pullImages(
				cmd,
				params,
				args,
				platform,
				status.Options{Debug: debug, Quiet: quiet},
			)
		},
	}
	command.Flags().StringVar(
		&platformValue,
		"platform",
		"",
		"Pull the Linux platform as os/architecture[/variant].",
	)
	return command
}

func pullImages(
	command *cobra.Command,
	params Params,
	references []string,
	platform ocispec.Platform,
	statusOptions status.Options,
) (returnErr error) {
	if params.Agent == nil {
		return fmt.Errorf("agent client is not configured")
	}
	if params.Status == nil {
		return fmt.Errorf("status presentation is not configured")
	}
	if err := params.Status.Begin(statusOptions); err != nil {
		return err
	}
	presentationActive := true
	defer func() {
		if presentationActive {
			params.Diagnostics.Logger("cli.image").DebugError(
				"finish image pull presentation",
				params.Status.Finish(nil),
			)
		}
	}()

	requests, err := normalizeImagePullRequests(references, platform)
	if err != nil {
		return exitcode.New(2, "%v", err)
	}

	connectOperation := params.Status.StartOperation(
		"Connecting to the Toby agent",
	)
	session, err := params.Agent.Connect(
		command.Context(),
		nil,
	)
	connectOperation.Finish(err)
	if err != nil {
		return err
	}
	defer func() {
		params.Diagnostics.Logger("cli.image").DebugError(
			"close image pull agent session",
			session.Close(),
		)
	}()

	resources, err := clientresource.NewRegistry(
		protocol.ResourceOCI,
		session,
		params.Diagnostics.Logger("clientresource.oci"),
	)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(
			context.Background(),
			imagePullReleaseTimeout,
		)
		defer cancel()
		params.Diagnostics.Logger("cli.image").DebugError(
			"release image pull resources",
			resources.Close(closeCtx),
		)
	}()

	bindings := make([]imagePullBinding, 0, len(requests))
	for _, request := range requests {
		clientID, err := resources.Acquire(command.Context(), request)
		if err != nil {
			return fmt.Errorf(
				"register OCI image %q: %w",
				request.Reference,
				err,
			)
		}
		bindings = append(bindings, imagePullBinding{
			config:   request,
			clientID: clientID,
		})
	}

	results := make([]error, len(bindings))
	var workers sync.WaitGroup
	for index, binding := range bindings {
		workers.Add(1)
		go func(index int, binding imagePullBinding) {
			defer workers.Done()

			reference := binding.config.Reference
			err := prepareclient.Follow(
				command.Context(),
				resources,
				binding.clientID,
				reference,
				prepareclient.Presentation{
					Start: func() *status.Operation {
						operation := params.Status.StartOperation(
							"Preparing OCI image " + reference,
						)
						operation.SetProgress(status.Progress{
							OCIAction:    "Preparing",
							OCIReference: reference,
						})
						return operation
					},
					CompleteLabel: "Pulled OCI image " + reference,
					Stdout:        command.OutOrStdout(),
					Stderr:        command.ErrOrStderr(),
					Logger: params.Diagnostics.Logger(
						"oci.prepare",
					),
				},
			)
			results[index] = err
		}(index, binding)
	}
	workers.Wait()
	if err := errors.Join(results...); err != nil {
		return err
	}
	presentationErr := params.Status.Finish(nil)
	presentationActive = false
	if presentationErr != nil {
		return presentationErr
	}

	return withImageStore(
		params,
		func(store *oci.Store) error {
			for _, request := range requests {
				info, err := store.InspectImage(
					command.Context(),
					request.Reference,
					request.Platform,
				)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(
					command.OutOrStdout(),
					info.ID,
				); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

func normalizeImagePullRequests(
	references []string,
	platform ocispec.Platform,
) ([]ociresource.Config, error) {
	if platform.OS == "" {
		platform.OS = "linux"
	}
	if platform.Architecture == "" {
		platform.Architecture = runtime.GOARCH
	}

	result := make([]ociresource.Config, 0, len(references))
	seen := make(map[string]bool, len(references))
	for _, reference := range references {
		request, err := ociresource.Normalize(ociresource.Config{
			Reference:  reference,
			Platform:   platform,
			PullPolicy: image.PullAlways,
		})
		if err != nil {
			return nil, err
		}
		identity, err := json.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf(
				"encode OCI pull request identity: %w",
				err,
			)
		}
		key := string(identity)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, request)
	}
	return result, nil
}
