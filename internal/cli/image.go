package cli

// Builds per-user OCI image pulling, inspection, filtering, and removal
// commands.

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/version"
)

const (
	imageCommandName        = "image"
	imagePullCommandName    = "pull"
	imageListCommandName    = "list"
	imageInspectCommandName = "inspect"
	imagePathCommandName    = "path"
	imageRemoveCommandName  = "remove"
	imagePruneCommandName   = "prune"
)

func isConfigFreeImageInvocation(arguments []string) bool {
	flags := rootFlagValues{managedTerminal: true}
	root := &cobra.Command{
		Use:              "toby",
		Version:          version.String(),
		SilenceUsage:     true,
		SilenceErrors:    true,
		TraverseChildren: true,
		Run:              func(*cobra.Command, []string) {},
	}
	root.SetArgs(append([]string(nil), arguments...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	addRootPersistentFlags(root, &flags)

	imageCommand := &cobra.Command{
		Use:  imageCommandName,
		Args: cobra.NoArgs,
		Run:  func(*cobra.Command, []string) {},
	}
	for _, name := range []string{
		imagePullCommandName,
		imageListCommandName,
		imageInspectCommandName,
		imagePathCommandName,
		imageRemoveCommandName,
		imagePruneCommandName,
	} {
		subcommand := &cobra.Command{
			Use:  name,
			Args: cobra.ArbitraryArgs,
			Run:  func(*cobra.Command, []string) {},
		}
		switch name {
		case imageListCommandName:
			subcommand.Aliases = []string{managementListAlias}
		case imageRemoveCommandName:
			subcommand.Aliases = []string{managementRemoveAlias}
		}
		switch name {
		case imagePullCommandName:
			subcommand.Flags().String("platform", "", "")
		case imageListCommandName:
			addImageFilterFlags(subcommand, &imageFilterFlagValues{})
		case imageInspectCommandName:
			subcommand.Flags().String("platform", "", "")
			subcommand.Flags().StringP("output", "o", "yaml", "")
		case imagePathCommandName:
			subcommand.Flags().String("platform", "", "")
		case imageRemoveCommandName:
			addImageFilterFlags(subcommand, &imageFilterFlagValues{})
			subcommand.Flags().Bool("force", false, "")
		case imagePruneCommandName:
			subcommand.Flags().Bool("force", false, "")
		}
		imageCommand.AddCommand(subcommand)
	}
	root.AddCommand(imageCommand)

	executed, err := root.ExecuteC()
	if err != nil && executed == nil {
		return false
	}
	return executed != nil &&
		(executed == imageCommand || executed.Parent() == imageCommand)
}

func newImageCommand(
	params Params,
	rootFlags *rootFlagValues,
) *cobra.Command {
	command := &cobra.Command{
		Use:   imageCommandName,
		Short: "Manage per-user OCI images.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newImagePullCommand(params, rootFlags),
		newImageListCommand(params),
		newImageInspectCommand(params),
		newImagePathCommand(params),
		newImageRemoveCommand(params),
		newImagePruneCommand(params),
	)
	return command
}

func newImageListCommand(params Params) *cobra.Command {
	flags := imageFilterFlagValues{}
	command := &cobra.Command{
		Use:     imageListCommandName,
		Aliases: []string{managementListAlias},
		Short:   "List per-user OCI image references and dangling objects.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter(cmd)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			return withImageStore(
				params,
				func(store *oci.Store) error {
					images, err := store.ListImages(
						cmd.Context(),
						filter,
					)
					if err != nil {
						return err
					}
					return writeImageList(cmd.OutOrStdout(), images)
				},
			)
		},
	}
	addImageFilterFlags(command, &flags)
	return command
}

func newImageInspectCommand(params Params) *cobra.Command {
	outputFormat := "yaml"
	platformValue := ""
	command := &cobra.Command{
		Use:   imageInspectCommandName + " <image>",
		Short: "Show one OCI image's metadata and native paths.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat != "yaml" && outputFormat != "json" {
				return exitcode.New(
					2,
					"unsupported output format %q; expected yaml or json",
					outputFormat,
				)
			}
			platform, err := parseImagePlatform(platformValue, true)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			return withImageStore(
				params,
				func(store *oci.Store) error {
					image, inspectErr := store.InspectImage(
						cmd.Context(),
						args[0],
						platform,
					)
					if inspectErr != nil && image.ID == "" {
						return inspectErr
					}
					outputErr := writeImageInspection(
						cmd.OutOrStdout(),
						image,
						outputFormat,
					)
					return errors.Join(inspectErr, outputErr)
				},
			)
		},
	}
	command.Flags().StringVar(
		&platformValue,
		"platform",
		"",
		"Select the Linux platform as os/architecture[/variant].",
	)
	command.Flags().StringVarP(
		&outputFormat,
		"output",
		"o",
		"yaml",
		"Set the output format to yaml or json.",
	)
	return command
}

func newImagePathCommand(params Params) *cobra.Command {
	platformValue := ""
	command := &cobra.Command{
		Use:   imagePathCommandName + " <image>",
		Short: "Print one OCI image's native rootfs path.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platform, err := parseImagePlatform(platformValue, true)
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			return withImageStore(
				params,
				func(store *oci.Store) error {
					image, err := store.InspectImage(
						cmd.Context(),
						args[0],
						platform,
					)
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(
						cmd.OutOrStdout(),
						image.RootfsPath,
					)
					return err
				},
			)
		},
	}
	command.Flags().StringVar(
		&platformValue,
		"platform",
		"",
		"Select the Linux platform as os/architecture[/variant].",
	)
	return command
}

func newImageRemoveCommand(params Params) *cobra.Command {
	flags := imageFilterFlagValues{}
	force := false
	command := &cobra.Command{
		Use:     imageRemoveCommandName + " [image]...",
		Aliases: []string{managementRemoveAlias},
		Short:   "Remove OCI image references and unused objects.",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withImageStore(
				params,
				func(store *oci.Store) error {
					images, err := selectImagesForRemoval(
						cmd.Context(),
						store,
						args,
						flags,
						cmd,
					)
					if err != nil {
						return err
					}
					if len(images) == 0 {
						return fmt.Errorf("no images match the selection")
					}
					return removeSelectedImages(
						cmd,
						store,
						images,
						force,
						force,
						params.Diagnostics.Logger("cli.image"),
					)
				},
			)
		},
	}
	addImageFilterFlags(command, &flags)
	command.Flags().BoolVar(
		&force,
		"force",
		false,
		"Remove references without confirmation, retaining busy objects.",
	)
	return command
}

func newImagePruneCommand(params Params) *cobra.Command {
	force := false
	command := &cobra.Command{
		Use:   imagePruneCommandName,
		Short: "Remove dangling OCI image objects that are not in use.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withImageStore(
				params,
				func(store *oci.Store) error {
					dangling := true
					images, err := store.ListImages(
						cmd.Context(),
						oci.ImageFilter{Dangling: &dangling},
					)
					if err != nil {
						return err
					}
					if len(images) == 0 {
						_, err := fmt.Fprintln(
							cmd.OutOrStdout(),
							"No dangling images.",
						)
						return err
					}
					return removeSelectedImages(
						cmd,
						store,
						images,
						force,
						false,
						params.Diagnostics.Logger("cli.image"),
					)
				},
			)
		},
	}
	command.Flags().BoolVar(
		&force,
		"force",
		false,
		"Prune dangling images without confirmation.",
	)
	return command
}

func withImageStore(
	params Params,
	run func(*oci.Store) error,
) (returnErr error) {
	store, err := oci.NewStore(
		params.Paths,
		params.Diagnostics,
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			params.Diagnostics.Logger("cli.image").DebugError(
				"close OCI image store",
				closeErr,
			)
		}
	}()

	return run(store)
}
