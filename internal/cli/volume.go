package cli

// Builds persistent-volume creation, filtering, inspection, and removal
// commands.

import (
	"errors"
	"fmt"
	"io"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/version"

	"github.com/spf13/cobra"
)

const (
	volumeCommandName        = "volume"
	volumeCreateCommandName  = "create"
	volumeListCommandName    = "list"
	volumeInspectCommandName = "inspect"
	volumePathCommandName    = "path"
	volumeRemoveCommandName  = "remove"
)

func isConfigFreeVolumeInvocation(arguments []string) bool {
	flags := rootFlagValues{
		managedTerminal: true,
	}
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

	volume := &cobra.Command{
		Use:  volumeCommandName,
		Args: cobra.NoArgs,
		Run:  func(*cobra.Command, []string) {},
	}
	for _, name := range []string{
		volumeCreateCommandName,
		volumeListCommandName,
		volumeInspectCommandName,
		volumePathCommandName,
		volumeRemoveCommandName,
	} {
		subcommand := &cobra.Command{
			Use:  name,
			Args: cobra.ArbitraryArgs,
			Run:  func(*cobra.Command, []string) {},
		}
		switch name {
		case volumeListCommandName:
			subcommand.Aliases = []string{managementListAlias}
		case volumeRemoveCommandName:
			subcommand.Aliases = []string{managementRemoveAlias}
		}
		if name == volumeInspectCommandName {
			subcommand.Flags().StringP("output", "o", "yaml", "")
		}
		addVolumeMetadataFlags(subcommand, &volumeMetadataFlagValues{})
		if name == volumeRemoveCommandName {
			subcommand.Flags().Bool("force", false, "")
		}
		volume.AddCommand(subcommand)
	}
	root.AddCommand(volume)

	executed, err := root.ExecuteC()
	if err != nil && executed == nil {
		return false
	}
	return executed != nil &&
		(executed == volume || executed.Parent() == volume)
}

func newVolumeCommand(params Params) *cobra.Command {
	command := &cobra.Command{
		Use:   volumeCommandName,
		Short: "Manage persistent Toby volumes.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newVolumeCreateCommand(params),
		newVolumeListCommand(params),
		newVolumeInspectCommand(params),
		newVolumePathCommand(params),
		newVolumeRemoveCommand(params),
	)
	return command
}

func newVolumeListCommand(params Params) *cobra.Command {
	flags := volumeMetadataFlagValues{}
	command := &cobra.Command{
		Use:     volumeListCommandName,
		Aliases: []string{managementListAlias},
		Short:   "List persistent Toby volumes.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter()
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			return withVolumeStore(
				params,
				func(store *storage.Store) error {
					volumes, err := store.ListVolumes(
						cmd.Context(),
						filter,
					)
					if err != nil {
						return err
					}
					return writeVolumeList(cmd.OutOrStdout(), volumes)
				},
			)
		},
	}
	addVolumeMetadataFlags(command, &flags)
	return command
}

func newVolumeCreateCommand(params Params) *cobra.Command {
	flags := volumeMetadataFlagValues{}
	command := &cobra.Command{
		Use:   volumeCreateCommandName,
		Short: "Create an empty persistent Toby volume.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := flags.spec()
			if err != nil {
				return exitcode.New(2, "%v", err)
			}
			return withVolumeStore(
				params,
				func(store *storage.Store) error {
					volume, err := store.CreateVolume(
						cmd.Context(),
						spec,
					)
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(
						cmd.OutOrStdout(),
						volume.ID,
					)
					return err
				},
			)
		},
	}
	addVolumeMetadataFlags(command, &flags)
	return command
}

func newVolumeInspectCommand(params Params) *cobra.Command {
	outputFormat := "yaml"
	flags := volumeMetadataFlagValues{}
	command := &cobra.Command{
		Use:   volumeInspectCommandName + " [volume-id]",
		Short: "Show one volume's metadata and native paths.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat != "yaml" && outputFormat != "json" {
				return exitcode.New(
					2,
					"unsupported output format %q; expected yaml or json",
					outputFormat,
				)
			}
			return withVolumeStore(
				params,
				func(store *storage.Store) error {
					volume, inspectErr := inspectSelectedVolume(
						cmd.Context(),
						store,
						args,
						flags,
					)
					outputErr := writeVolumeInspection(
						cmd.OutOrStdout(),
						volume,
						outputFormat,
					)
					return errors.Join(inspectErr, outputErr)
				},
			)
		},
	}
	command.Flags().StringVarP(
		&outputFormat,
		"output",
		"o",
		"yaml",
		"Set the output format to yaml or json.",
	)
	addVolumeMetadataFlags(command, &flags)
	return command
}

func newVolumePathCommand(params Params) *cobra.Command {
	flags := volumeMetadataFlagValues{}
	command := &cobra.Command{
		Use:   volumePathCommandName + " [volume-id]",
		Short: "Print one volume's native data path.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVolumeStore(
				params,
				func(store *storage.Store) error {
					volume, err := inspectSelectedVolume(
						cmd.Context(),
						store,
						args,
						flags,
					)
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(
						cmd.OutOrStdout(),
						volume.DataPath,
					)
					return err
				},
			)
		},
	}
	addVolumeMetadataFlags(command, &flags)
	return command
}

func newVolumeRemoveCommand(params Params) *cobra.Command {
	flags := volumeMetadataFlagValues{}
	force := false
	command := &cobra.Command{
		Use:     volumeRemoveCommandName + " [volume-id]...",
		Aliases: []string{managementRemoveAlias},
		Short:   "Remove persistent volumes that are not in use.",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVolumeStore(
				params,
				func(store *storage.Store) error {
					volumes, err := selectVolumesForRemoval(
						cmd.Context(),
						store,
						args,
						flags,
					)
					if err != nil {
						return err
					}
					if len(volumes) == 0 {
						return fmt.Errorf("no volumes match the selection")
					}
					if !force {
						confirmed, err := confirmVolumeRemoval(
							cmd.Context(),
							cmd.InOrStdin(),
							cmd.OutOrStdout(),
							volumes,
						)
						if err != nil {
							return err
						}
						if !confirmed {
							_, err := fmt.Fprintln(
								cmd.OutOrStdout(),
								"Volume removal cancelled.",
							)
							return err
						}
					}
					if terminalStream(cmd.OutOrStdout()) {
						return runVolumeRemovalProgress(
							cmd.Context(),
							cmd.OutOrStdout(),
							store,
							volumes,
							params.Diagnostics.Logger("cli.volume"),
						)
					}

					return removeVolumesPlain(
						cmd.Context(),
						cmd.OutOrStdout(),
						store,
						volumes,
					)
				},
			)
		},
	}
	addVolumeMetadataFlags(command, &flags)
	command.Flags().BoolVar(
		&force,
		"force",
		false,
		"Remove matching volumes without confirmation.",
	)
	return command
}

func withVolumeStore(
	params Params,
	run func(*storage.Store) error,
) (returnErr error) {
	store, err := storage.NewStore(
		params.Paths,
		storage.DefaultLimits(),
		params.Diagnostics,
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			params.Diagnostics.Logger("cli.volume").DebugError(
				"close volume store",
				closeErr,
			)
		}
	}()

	return run(store)
}
