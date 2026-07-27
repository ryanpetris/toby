package cli

// Resolves volume metadata flags as complete identities or partial filters.

import (
	"context"

	"github.com/spf13/cobra"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/storage"
)

type volumeMetadataFlagValues struct {
	volumeType string
	name       string
	profile    string
	purpose    string
}

func addVolumeMetadataFlags(
	command *cobra.Command,
	values *volumeMetadataFlagValues,
) {
	command.Flags().StringVar(
		&values.volumeType,
		"type",
		"",
		"Select the home or tool volume type.",
	)
	command.Flags().StringVar(
		&values.name,
		"name",
		"",
		"Select the exact volume name.",
	)
	command.Flags().StringVar(
		&values.profile,
		"profile",
		"",
		"Select the exact volume profile.",
	)
	command.Flags().StringVar(
		&values.purpose,
		"purpose",
		"",
		"Select the exact tool-volume purpose.",
	)
}

func (v volumeMetadataFlagValues) changed() bool {
	return v.volumeType != "" ||
		v.name != "" ||
		v.profile != "" ||
		v.purpose != ""
}

func (v volumeMetadataFlagValues) filter() (storage.VolumeFilter, error) {
	return (storage.VolumeFilter{
		Type:    storage.VolumeType(v.volumeType),
		Name:    v.name,
		Profile: v.profile,
		Purpose: v.purpose,
	}).Normalize()
}

func (v volumeMetadataFlagValues) spec() (storage.VolumeSpec, error) {
	return (storage.VolumeSpec{
		Type:    storage.VolumeType(v.volumeType),
		Name:    v.name,
		Profile: v.profile,
		Purpose: v.purpose,
	}).Normalize()
}

func inspectSelectedVolume(
	ctx context.Context,
	store *storage.Store,
	args []string,
	flags volumeMetadataFlagValues,
) (storage.VolumeInfo, error) {
	switch {
	case len(args) == 1 && flags.changed():
		return storage.VolumeInfo{}, exitcode.New(
			2,
			"volume ID and metadata selector flags are mutually exclusive",
		)
	case len(args) == 1:
		return store.InspectVolume(ctx, args[0])
	case flags.changed():
		spec, err := flags.spec()
		if err != nil {
			return storage.VolumeInfo{},
				exitcode.New(2, "%v", err)
		}
		return store.InspectVolumeBySpec(ctx, spec)
	default:
		return storage.VolumeInfo{}, exitcode.New(
			2,
			"specify a volume ID or metadata selector flags",
		)
	}
}

func selectVolumesForRemoval(
	ctx context.Context,
	store *storage.Store,
	args []string,
	flags volumeMetadataFlagValues,
) ([]storage.VolumeInfo, error) {
	if len(args) != 0 && flags.changed() {
		return nil, exitcode.New(
			2,
			"volume IDs and metadata filter flags are mutually exclusive",
		)
	}
	if len(args) != 0 {
		volumes := make([]storage.VolumeInfo, 0, len(args))
		seen := make(map[string]bool, len(args))
		for _, selector := range args {
			volume, err := store.InspectVolume(ctx, selector)
			if err != nil && volume.ID == "" {
				return nil, err
			}
			if !seen[volume.ID] {
				seen[volume.ID] = true
				volumes = append(volumes, volume)
			}
		}
		return volumes, nil
	}
	if !flags.changed() {
		return nil, exitcode.New(
			2,
			"specify volume IDs or at least one metadata filter",
		)
	}

	filter, err := flags.filter()
	if err != nil {
		return nil, exitcode.New(2, "%v", err)
	}
	return store.ListVolumes(ctx, filter)
}
