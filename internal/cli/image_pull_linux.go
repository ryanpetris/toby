package cli

// Defines registry-backed image pulls.

import (
	"encoding/json"
	"fmt"
	"runtime"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"

	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/status"
)

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
			options, err := imageStatusOptions(rootFlags)
			if err != nil {
				return err
			}
			return pullImages(
				cmd,
				params,
				args,
				platform,
				options,
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
	requests, err := normalizeImagePullRequests(references, platform)
	if err != nil {
		return exitcode.New(2, "%v", err)
	}
	return prepareImageRequests(
		command,
		params,
		requests,
		statusOptions,
		func(reference string) string {
			return "Pulled OCI image " + reference
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
			Source:     imagesource.Registry,
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
