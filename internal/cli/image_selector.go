package cli

// Resolves image filter flags, platform values, and removal selections.

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/oci"
)

type imageFilterFlagValues struct {
	reference string
	platform  string
	digest    string
	dangling  bool
}

func addImageFilterFlags(
	command *cobra.Command,
	values *imageFilterFlagValues,
) {
	command.Flags().StringVar(
		&values.reference,
		"reference",
		"",
		"Select an exact normalized OCI image reference.",
	)
	command.Flags().StringVar(
		&values.platform,
		"platform",
		"",
		"Select the Linux platform as os/architecture[/variant].",
	)
	command.Flags().StringVar(
		&values.digest,
		"digest",
		"",
		"Select an exact sha256 manifest digest.",
	)
	command.Flags().BoolVar(
		&values.dangling,
		"dangling",
		false,
		"Filter by dangling state; --dangling selects unreferenced objects.",
	)
}

func (v imageFilterFlagValues) changed(command *cobra.Command) bool {
	return v.reference != "" ||
		v.platform != "" ||
		v.digest != "" ||
		command.Flags().Changed("dangling")
}

func (v imageFilterFlagValues) changedExceptPlatform(
	command *cobra.Command,
) bool {
	return v.reference != "" ||
		v.digest != "" ||
		command.Flags().Changed("dangling")
}

func (v imageFilterFlagValues) filter(
	command *cobra.Command,
) (oci.ImageFilter, error) {
	platform, err := parseImagePlatform(v.platform, false)
	if err != nil {
		return oci.ImageFilter{}, err
	}

	var manifestDigest digest.Digest
	if strings.TrimSpace(v.digest) != "" {
		manifestDigest = digest.Digest(strings.TrimSpace(v.digest))
		if err := manifestDigest.Validate(); err != nil {
			return oci.ImageFilter{}, fmt.Errorf(
				"invalid OCI manifest digest %q: %w",
				v.digest,
				err,
			)
		}
	}

	var dangling *bool
	if command.Flags().Changed("dangling") {
		value := v.dangling
		dangling = &value
	}
	return oci.ImageFilter{
		Reference: strings.TrimSpace(v.reference),
		Platform:  platform,
		Digest:    manifestDigest,
		Dangling:  dangling,
	}, nil
}

func parseImagePlatform(
	value string,
	defaultCurrent bool,
) (ocispec.Platform, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if !defaultCurrent {
			return ocispec.Platform{}, nil
		}
		return ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		}, nil
	}

	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 ||
		parts[0] == "" || parts[1] == "" ||
		(len(parts) == 3 && parts[2] == "") {
		return ocispec.Platform{}, fmt.Errorf(
			"invalid OCI platform %q; expected os/architecture[/variant]",
			value,
		)
	}
	if parts[0] != "linux" {
		return ocispec.Platform{}, fmt.Errorf(
			"unsupported OCI platform OS %q",
			parts[0],
		)
	}

	platform := ocispec.Platform{
		OS:           parts[0],
		Architecture: parts[1],
	}
	if len(parts) == 3 {
		platform.Variant = parts[2]
	}
	return platform, nil
}

func selectImagesForRemoval(
	ctx context.Context,
	store *oci.Store,
	args []string,
	flags imageFilterFlagValues,
	command *cobra.Command,
) ([]oci.ImageInfo, error) {
	if len(args) != 0 && flags.changedExceptPlatform(command) {
		return nil, exitcode.New(
			2,
			"image selectors and filter flags other than --platform are mutually exclusive",
		)
	}
	if len(args) != 0 {
		platform, err := parseImagePlatform(flags.platform, true)
		if err != nil {
			return nil, exitcode.New(2, "%v", err)
		}

		images := make([]oci.ImageInfo, 0, len(args))
		seen := make(map[string]bool, len(args))
		for _, selector := range args {
			image, err := store.InspectImage(ctx, selector, platform)
			if err != nil {
				return nil, err
			}
			key := string(image.Kind) + "\x00" + image.ID
			if !seen[key] {
				seen[key] = true
				images = append(images, image)
			}
		}
		return images, nil
	}
	if !flags.changed(command) {
		return nil, exitcode.New(
			2,
			"specify image selectors or at least one image filter",
		)
	}

	filter, err := flags.filter(command)
	if err != nil {
		return nil, exitcode.New(2, "%v", err)
	}
	return store.ListImages(ctx, filter)
}
