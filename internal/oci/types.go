package oci

// Declares image-preparation requests, progress, and metadata consumed by
// native sandboxes after the image has been prepared.

import (
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

// ProgressPhase identifies one image-preparation phase.
type ProgressPhase string

const (
	// ProgressResolving means image metadata is being resolved.
	ProgressResolving ProgressPhase = "resolving"
	// ProgressDownloading means image content is being downloaded.
	ProgressDownloading ProgressPhase = "downloading"
	// ProgressExtracting means the root filesystem is being extracted.
	ProgressExtracting ProgressPhase = "extracting"
)

// Progress is one complete, absolute image-preparation snapshot.
type Progress struct {
	Phase          ProgressPhase
	CompletedBytes int64
	TotalBytes     int64
	CompletedItems int64
	TotalItems     int64
}

// ProgressReporter receives absolute progress snapshots. Implementations must
// support calls from concurrent layer downloads.
type ProgressReporter func(Progress) error

// Request selects one exact platform, pull policy, and progress destination.
type Request struct {
	Source     imagesource.Kind
	Reference  string
	Archive    string
	Build      imagesource.BuildConfig
	Platform   ocispec.Platform
	PullPolicy image.PullPolicy
	Progress   ProgressReporter
	Stdout     io.Writer
	Stderr     io.Writer
}

// RuntimeConfig is the OCI image configuration consumed by sandbox planning.
type RuntimeConfig struct {
	Environment []string          `json:"environment"`
	Workdir     string            `json:"workdir"`
	Entrypoint  []string          `json:"entrypoint"`
	Command     []string          `json:"command"`
	User        string            `json:"user"`
	Labels      map[string]string `json:"labels"`
}

// Spec identifies one immutable unpacked root filesystem and its runtime
// configuration.
type Spec struct {
	Platform ocispec.Platform   `json:"platform"`
	Manifest ocispec.Descriptor `json:"manifest"`
	Config   ocispec.Descriptor `json:"config"`
	Runtime  RuntimeConfig      `json:"runtime"`
}

// Metadata identifies the requested image reference and the immutable object
// selected for it.
type Metadata struct {
	Reference  string
	Repository string
	Spec
}

func cloneSpec(spec Spec) Spec {
	clone := spec
	clone.Platform.OSFeatures = append(
		[]string(nil),
		spec.Platform.OSFeatures...,
	)
	clone.Manifest = cloneDescriptor(spec.Manifest)
	clone.Config = cloneDescriptor(spec.Config)
	clone.Runtime.Environment = append(
		[]string(nil),
		spec.Runtime.Environment...,
	)
	clone.Runtime.Entrypoint = append(
		[]string(nil),
		spec.Runtime.Entrypoint...,
	)
	clone.Runtime.Command = append(
		[]string(nil),
		spec.Runtime.Command...,
	)
	if spec.Runtime.Labels != nil {
		clone.Runtime.Labels = make(
			map[string]string,
			len(spec.Runtime.Labels),
		)
		for key, value := range spec.Runtime.Labels {
			clone.Runtime.Labels[key] = value
		}
	}

	return clone
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.Spec = cloneSpec(metadata.Spec)
	return metadata
}

func cloneDescriptor(descriptor ocispec.Descriptor) ocispec.Descriptor {
	clone := descriptor
	clone.URLs = append([]string(nil), descriptor.URLs...)
	clone.Data = append([]byte(nil), descriptor.Data...)
	if descriptor.Annotations != nil {
		clone.Annotations = make(
			map[string]string,
			len(descriptor.Annotations),
		)
		for key, value := range descriptor.Annotations {
			clone.Annotations[key] = value
		}
	}
	if descriptor.Platform != nil {
		platform := *descriptor.Platform
		platform.OSFeatures = append(
			[]string(nil),
			descriptor.Platform.OSFeatures...,
		)
		clone.Platform = &platform
	}

	return clone
}
