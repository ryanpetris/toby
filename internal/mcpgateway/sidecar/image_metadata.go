package sidecar

// Normalizes immutable image metadata and the sidecar environment.

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"petris.dev/toby/internal/sandbox/bwrap"
)

func imageMetadata(prepared Image) (Metadata, error) {
	spec := prepared.Spec()
	resolved := prepared.Metadata()
	repository := resolved.Repository
	if repository == "" || spec.Manifest.Digest.String() == "" ||
		spec.Config.Digest.String() == "" {
		return Metadata{}, fmt.Errorf(
			"sidecar image metadata is incomplete",
		)
	}
	workdir := spec.Runtime.Workdir
	if workdir == "" {
		workdir = "/"
	}
	if !path.IsAbs(workdir) || path.Clean(workdir) != workdir {
		return Metadata{}, fmt.Errorf(
			"sidecar image workdir is invalid",
		)
	}

	return Metadata{
		ImmutableImage: repository + "@" + spec.Manifest.Digest.String(),
		ManifestDigest: spec.Manifest.Digest.String(),
		RootFSDigest:   spec.Config.Digest.String(),
		Workdir:        workdir,
	}, nil
}

func sidecarEnvironment(
	imageEnvironment []string,
	overrides map[string]string,
) ([]bwrap.EnvironmentVariable, error) {
	values := make(map[string]string, len(imageEnvironment)+len(overrides))
	for _, entry := range imageEnvironment {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			return nil, fmt.Errorf(
				"sidecar image environment contains an invalid entry",
			)
		}
		if name == "HOME" || name == "TOBY_SANDBOX" {
			continue
		}
		values[name] = value
	}
	for name, value := range overrides {
		if name == "HOME" || name == "TOBY_SANDBOX" {
			return nil, fmt.Errorf(
				"sidecar environment variable %q is runtime-controlled",
				name,
			)
		}
		values[name] = value
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]bwrap.EnvironmentVariable, len(names))
	for index, name := range names {
		result[index] = bwrap.EnvironmentVariable{
			Name:  name,
			Value: values[name],
		}
	}

	return result, nil
}
