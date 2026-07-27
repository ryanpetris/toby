//go:build linux

package cli

// Builds one Dockerfile context into an OCI archive without importing it.

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/imagesource"
)

func newImageBuildCommand(
	params Params,
	rootFlags *rootFlagValues,
) *cobra.Command {
	dockerfileValue := ""
	outputValue := ""
	platformValue := ""
	command := &cobra.Command{
		Use:   imageBuildCommandName + " [context]",
		Short: "Build a Dockerfile context into an OCI archive.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			if params.Status == nil {
				return fmt.Errorf("status presentation is not configured")
			}
			if outputValue == "" {
				return exitcode.New(2, "--output is required")
			}
			platform, err := parseImagePlatform(platformValue, true)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}

			contextValue := "."
			if len(args) != 0 {
				contextValue = args[0]
			}
			contextValue = config.ExpandHome(
				contextValue,
				params.Paths.Home,
			)
			contextPath, err := filepath.Abs(contextValue)
			if err != nil {
				return exitcode.New(
					2,
					"resolve build context: %v",
					err,
				)
			}
			dockerfilePath := dockerfileValue
			if dockerfilePath == "" {
				dockerfilePath = filepath.Join(
					contextPath,
					"Dockerfile",
				)
			} else {
				dockerfilePath = config.ExpandHome(
					dockerfilePath,
					params.Paths.Home,
				)
			}
			if !filepath.IsAbs(dockerfilePath) {
				dockerfilePath = filepath.Join(
					contextPath,
					dockerfilePath,
				)
			}
			dockerfilePath, err = filepath.Abs(dockerfilePath)
			if err != nil {
				return exitcode.New(
					2,
					"resolve Dockerfile: %v",
					err,
				)
			}
			outputPath, err := filepath.Abs(config.ExpandHome(
				outputValue,
				params.Paths.Home,
			))
			if err != nil {
				return exitcode.New(
					2,
					"resolve OCI archive output: %v",
					err,
				)
			}

			options, err := imageStatusOptions(rootFlags)
			if err != nil {
				return err
			}
			if err := params.Status.Begin(options); err != nil {
				return err
			}
			defer func() {
				returnErr = params.Status.Finish(returnErr)
			}()

			operation := params.Status.StartOperation(
				"Building OCI archive " + filepath.Base(outputPath),
			)
			output := operation.Writer(cmd.ErrOrStderr())
			err = oci.BuildArchive(
				cmd.Context(),
				imagesource.BuildConfig{
					Context:    filepath.Clean(contextPath),
					Dockerfile: filepath.Clean(dockerfilePath),
				},
				platform,
				filepath.Clean(outputPath),
				output,
				output,
			)
			if err != nil {
				operation.Finish(err)
				return err
			}
			operation.Complete(
				"Built OCI archive " + filepath.Base(outputPath),
			)
			return nil
		},
	}
	command.Flags().StringVarP(
		&dockerfileValue,
		"file",
		"f",
		"",
		"Use this Dockerfile path relative to the build context.",
	)
	command.Flags().StringVarP(
		&outputValue,
		"output",
		"o",
		"",
		"Write the OCI image-layout tar to this path.",
	)
	command.Flags().StringVar(
		&platformValue,
		"platform",
		"",
		"Build the Linux platform as os/architecture[/variant].",
	)
	return command
}
