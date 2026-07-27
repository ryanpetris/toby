//go:build linux

package cli

// Imports one OCI image-layout archive into the per-user image store.

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

func newImageImportCommand(
	params Params,
	rootFlags *rootFlagValues,
) *cobra.Command {
	platformValue := ""
	command := &cobra.Command{
		Use: imageImportCommandName +
			" <archive> <reference>",
		Short: "Import an OCI image-layout archive into the per-user store.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			platform, err := parseImagePlatform(platformValue, true)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			archive, err := resolveImageInputPath(
				args[0],
				params.Paths.Home,
			)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			request, err := ociresource.Normalize(
				ociresource.Config{
					Source:     imagesource.Archive,
					Reference:  args[1],
					Archive:    archive,
					Platform:   platform,
					PullPolicy: image.PullAlways,
				},
			)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}

			options, err := imageStatusOptions(rootFlags)
			if err != nil {
				return err
			}
			return prepareImageRequests(
				cmd,
				params,
				[]ociresource.Config{request},
				options,
				func(reference string) string {
					return "Imported OCI image " + reference
				},
			)
		},
	}
	command.Flags().StringVar(
		&platformValue,
		"platform",
		"",
		"Import the Linux platform as os/architecture[/variant].",
	)
	return command
}

func resolveImageInputPath(value string, home string) (string, error) {
	value = config.ExpandHome(value, home)
	return filepath.Abs(value)
}
