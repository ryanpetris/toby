package oci

// Reads the bounded manifest and image configuration from an OCI layout and
// verifies their descriptor digests before publication.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	maximumIndexBytes    = 4 << 20
	maximumManifestBytes = 4 << 20
	maximumConfigBytes   = 16 << 20
	layoutReferenceName  = "root"
)

type layoutImage struct {
	spec     Spec
	manifest ocispec.Manifest
}

func (s *Store) readLayoutImage(
	layoutPath string,
	platform ocispec.Platform,
) (layoutImage, error) {
	indexData, err := s.readBoundedFile(
		filepath.Join(layoutPath, "index.json"),
		maximumIndexBytes,
	)
	if err != nil {
		return layoutImage{}, fmt.Errorf("read OCI layout index: %w", err)
	}

	var index ocispec.Index
	if err := decodeJSON(indexData, &index); err != nil {
		return layoutImage{}, fmt.Errorf("decode OCI layout index: %w", err)
	}
	manifestDescriptor, err := selectLayoutManifest(index)
	if err != nil {
		return layoutImage{}, err
	}
	manifestData, err := s.readLayoutBlob(
		layoutPath,
		manifestDescriptor,
		maximumManifestBytes,
	)
	if err != nil {
		return layoutImage{}, fmt.Errorf("read OCI image manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := decodeJSON(manifestData, &manifest); err != nil {
		return layoutImage{}, fmt.Errorf("decode OCI image manifest: %w", err)
	}
	configData, err := s.readLayoutBlob(
		layoutPath,
		manifest.Config,
		maximumConfigBytes,
	)
	if err != nil {
		return layoutImage{}, fmt.Errorf(
			"read OCI image configuration: %w",
			err,
		)
	}

	var imageConfig ocispec.Image
	if err := decodeJSON(configData, &imageConfig); err != nil {
		return layoutImage{}, fmt.Errorf(
			"decode OCI image configuration: %w",
			err,
		)
	}
	if imageConfig.OS != "" && imageConfig.OS != platform.OS {
		return layoutImage{}, fmt.Errorf(
			"OCI image configuration OS %q does not match selected %q",
			imageConfig.OS,
			platform.OS,
		)
	}
	if imageConfig.Architecture != "" &&
		imageConfig.Architecture != platform.Architecture {
		return layoutImage{}, fmt.Errorf(
			"OCI image configuration architecture %q does not match selected %q",
			imageConfig.Architecture,
			platform.Architecture,
		)
	}

	labels := imageConfig.Config.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	return layoutImage{
		spec: cloneSpec(Spec{
			Platform: platform,
			Manifest: descriptorIdentity(manifestDescriptor),
			Config:   descriptorIdentity(manifest.Config),
			Runtime: RuntimeConfig{
				Environment: imageConfig.Config.Env,
				Workdir:     imageConfig.Config.WorkingDir,
				Entrypoint:  imageConfig.Config.Entrypoint,
				Command:     imageConfig.Config.Cmd,
				User:        imageConfig.Config.User,
				Labels:      labels,
			},
		}),
		manifest: manifest,
	}, nil
}

func descriptorIdentity(
	descriptor ocispec.Descriptor,
) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType: descriptor.MediaType,
		Digest:    descriptor.Digest,
		Size:      descriptor.Size,
	}
}

func selectLayoutManifest(layoutIndex ocispec.Index) (
	ocispec.Descriptor,
	error,
) {
	var selected *ocispec.Descriptor
	for index := range layoutIndex.Manifests {
		descriptor := &layoutIndex.Manifests[index]
		if descriptor.Annotations[ocispec.AnnotationRefName] !=
			layoutReferenceName {
			continue
		}
		if selected != nil {
			return ocispec.Descriptor{}, fmt.Errorf(
				"OCI layout contains multiple %q references",
				layoutReferenceName,
			)
		}
		selected = descriptor
	}
	if selected == nil && len(layoutIndex.Manifests) == 1 {
		selected = &layoutIndex.Manifests[0]
	}
	if selected == nil {
		return ocispec.Descriptor{}, fmt.Errorf(
			"OCI layout does not contain the %q image reference",
			layoutReferenceName,
		)
	}
	if selected.MediaType != ocispec.MediaTypeImageManifest &&
		selected.MediaType !=
			"application/vnd.docker.distribution.manifest.v2+json" {
		return ocispec.Descriptor{}, fmt.Errorf(
			"OCI layout reference has unsupported media type %q",
			selected.MediaType,
		)
	}

	return cloneDescriptor(*selected), nil
}

func (s *Store) readLayoutBlob(
	layoutPath string,
	descriptor ocispec.Descriptor,
	limit int64,
) ([]byte, error) {
	if err := descriptor.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid descriptor digest: %w", err)
	}
	if descriptor.Digest.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf(
			"unsupported descriptor digest algorithm %q",
			descriptor.Digest.Algorithm(),
		)
	}
	if descriptor.Size < 0 || descriptor.Size > limit {
		return nil, fmt.Errorf(
			"descriptor size %d exceeds limit %d",
			descriptor.Size,
			limit,
		)
	}

	data, err := s.readBoundedFile(
		filepath.Join(
			layoutPath,
			"blobs",
			"sha256",
			descriptor.Digest.Encoded(),
		),
		limit,
	)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != descriptor.Size {
		return nil, fmt.Errorf(
			"descriptor size is %d, expected %d",
			len(data),
			descriptor.Size,
		)
	}
	if digest.SHA256.FromBytes(data) != descriptor.Digest {
		return nil, fmt.Errorf(
			"descriptor content does not match digest %s",
			descriptor.Digest,
		)
	}

	return data, nil
}

func (s *Store) readBoundedFile(
	name string,
	limit int64,
) (result []byte, returnErr error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"close bounded OCI input file",
			file.Close(),
			"path",
			name,
		)
	}()

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}

	return data, nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var trailing any
	err := decoder.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("document contains multiple JSON values")
		}
		return err
	}

	return nil
}
