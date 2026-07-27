package cli

// Coordinates agent-owned image preparation and result presentation.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/prepareclient"
	"petris.dev/toby/internal/status"
)

const imagePreparationReleaseTimeout = 15 * time.Second

type imagePreparationBinding struct {
	config   ociresource.Config
	clientID protocol.ClientResourceID
}

func prepareImageRequests(
	command *cobra.Command,
	params Params,
	requests []ociresource.Config,
	statusOptions status.Options,
	completeLabel func(string) string,
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
				"finish image preparation presentation",
				params.Status.Finish(nil),
			)
		}
	}()

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
			"close image preparation agent session",
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
			imagePreparationReleaseTimeout,
		)
		defer cancel()
		params.Diagnostics.Logger("cli.image").DebugError(
			"release image preparation resources",
			resources.Close(closeCtx),
		)
	}()

	bindings := make(
		[]imagePreparationBinding,
		0,
		len(requests),
	)
	for _, request := range requests {
		clientID, err := resources.Acquire(command.Context(), request)
		if err != nil {
			return fmt.Errorf(
				"register OCI image %q: %w",
				request.Reference,
				err,
			)
		}
		bindings = append(bindings, imagePreparationBinding{
			config:   request,
			clientID: clientID,
		})
	}

	results := make([]error, len(bindings))
	var workers sync.WaitGroup
	for index, binding := range bindings {
		workers.Add(1)
		go func(index int, binding imagePreparationBinding) {
			defer workers.Done()

			reference := binding.config.Reference
			err := prepareclient.Follow(
				command.Context(),
				resources,
				binding.clientID,
				reference,
				prepareclient.Presentation{
					Start: func() *status.Operation {
						return params.Status.StartOperation(
							"Preparing OCI image " + reference,
						)
					},
					CompleteLabel: completeLabel(reference),
					Stdout:        command.ErrOrStderr(),
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

func imageStatusOptions(
	rootFlags *rootFlagValues,
) (status.Options, error) {
	var options status.Options
	if rootFlags != nil {
		options.Debug = rootFlags.debug
		options.Quiet = rootFlags.quiet
	}
	if options.Debug && options.Quiet {
		return status.Options{}, exitcode.New(
			2,
			"--debug and --quiet are mutually exclusive",
		)
	}
	return options, nil
}
