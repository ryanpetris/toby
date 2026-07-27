package oci

// Encodes schema-1 immutable object metadata and mutable reference mappings.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const maximumMetadataBytes = 1 << 20

type objectMetadata struct {
	SchemaVersion int  `json:"schema_version"`
	Spec          Spec `json:"spec"`
}

type referenceRecord struct {
	SchemaVersion int              `json:"schema_version"`
	Reference     string           `json:"reference"`
	Platform      ocispec.Platform `json:"platform"`
	Object        string           `json:"object"`
}

func (s *Store) writeObjectMetadata(directory string, spec Spec) error {
	data, err := encodeMetadata(objectMetadata{
		SchemaVersion: metadataSchemaVersion,
		Spec:          cloneSpec(spec),
	})
	if err != nil {
		return fmt.Errorf("encode OCI object metadata: %w", err)
	}

	name := filepath.Join(directory, "metadata.json")
	file, err := os.OpenFile(
		name,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		metadataFileMode,
	)
	if err != nil {
		return fmt.Errorf("create OCI object metadata: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		s.logger.DebugError("close OCI object metadata", file.Close())
		return fmt.Errorf("write OCI object metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		s.logger.DebugError("close OCI object metadata", file.Close())
		return fmt.Errorf("sync OCI object metadata: %w", err)
	}
	s.logger.DebugError("close OCI object metadata", file.Close())

	return nil
}

func (s *Store) readObjectMetadata(directory string) (objectMetadata, error) {
	data, err := s.readBoundedFile(
		filepath.Join(directory, "metadata.json"),
		maximumMetadataBytes,
	)
	if err != nil {
		return objectMetadata{}, err
	}

	var metadata objectMetadata
	if err := decodeMetadata(data, &metadata); err != nil {
		return objectMetadata{}, err
	}
	if metadata.SchemaVersion != metadataSchemaVersion {
		return objectMetadata{}, fmt.Errorf(
			"unsupported OCI object metadata schema %d",
			metadata.SchemaVersion,
		)
	}
	if err := validatePublishedSpec(metadata.Spec); err != nil {
		return objectMetadata{}, fmt.Errorf(
			"invalid OCI object metadata: %w",
			err,
		)
	}

	return metadata, nil
}

func validatePublishedSpec(spec Spec) error {
	if spec.Platform.OS == "" || spec.Platform.Architecture == "" {
		return fmt.Errorf("platform OS and architecture are required")
	}
	if err := validateIdentityDescriptor(
		spec.Manifest,
		"manifest",
		ocispec.MediaTypeImageManifest,
	); err != nil {
		return err
	}
	if err := validateIdentityDescriptor(
		spec.Config,
		"config",
		ocispec.MediaTypeImageConfig,
	); err != nil {
		return err
	}
	if spec.Runtime.Labels == nil {
		return fmt.Errorf("runtime labels must be a map")
	}
	if spec.Runtime.Workdir != "" &&
		(!path.IsAbs(spec.Runtime.Workdir) ||
			path.Clean(spec.Runtime.Workdir) != spec.Runtime.Workdir) {
		return fmt.Errorf(
			"runtime working directory must be clean and absolute",
		)
	}
	values := []string{spec.Runtime.Workdir, spec.Runtime.User}
	values = append(values, spec.Runtime.Environment...)
	values = append(values, spec.Runtime.Entrypoint...)
	values = append(values, spec.Runtime.Command...)
	for _, value := range values {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("runtime configuration contains invalid text")
		}
	}
	for key, value := range spec.Runtime.Labels {
		if key == "" ||
			!utf8.ValidString(key) ||
			!utf8.ValidString(value) ||
			strings.ContainsRune(key, 0) ||
			strings.ContainsRune(value, 0) {
			return fmt.Errorf("runtime labels contain invalid text")
		}
	}

	return nil
}

func validateIdentityDescriptor(
	descriptor ocispec.Descriptor,
	label string,
	mediaType string,
) error {
	if descriptor.MediaType != mediaType {
		return fmt.Errorf(
			"%s media type is %q, want %q",
			label,
			descriptor.MediaType,
			mediaType,
		)
	}
	if err := descriptor.Digest.Validate(); err != nil {
		return fmt.Errorf("%s digest: %w", label, err)
	}
	if descriptor.Digest.Algorithm() != digest.SHA256 {
		return fmt.Errorf(
			"%s digest algorithm is %q, want sha256",
			label,
			descriptor.Digest.Algorithm(),
		)
	}
	if descriptor.Size < 0 {
		return fmt.Errorf("%s size is negative", label)
	}
	if descriptor.ArtifactType != "" ||
		descriptor.Data != nil ||
		descriptor.URLs != nil ||
		descriptor.Annotations != nil ||
		descriptor.Platform != nil {
		return fmt.Errorf(
			"%s contains non-identity descriptor fields",
			label,
		)
	}

	return nil
}

func (s *Store) publishReference(
	request normalizedRequest,
	object string,
) error {
	data, err := encodeMetadata(referenceRecord{
		SchemaVersion: metadataSchemaVersion,
		Reference:     request.reference,
		Platform:      request.platform,
		Object:        object,
	})
	if err != nil {
		return fmt.Errorf("encode OCI reference metadata: %w", err)
	}
	if err := s.root.ReplaceFile(
		filepath.Join("references", request.key+".json"),
		data,
		metadataFileMode,
	); err != nil {
		return fmt.Errorf(
			"publish OCI reference %q: %w",
			request.reference,
			err,
		)
	}

	return nil
}

func (s *Store) readReference(
	request normalizedRequest,
) (referenceRecord, bool, error) {
	data, err := s.root.ReadFile(
		filepath.Join("references", request.key+".json"),
		maximumMetadataBytes,
	)
	if errors.Is(err, os.ErrNotExist) {
		return referenceRecord{}, false, nil
	}
	if err != nil {
		return referenceRecord{}, false, fmt.Errorf(
			"read OCI reference %q: %w",
			request.reference,
			err,
		)
	}

	var record referenceRecord
	if err := decodeMetadata(data, &record); err != nil {
		return referenceRecord{}, false, fmt.Errorf(
			"decode OCI reference %q: %w",
			request.reference,
			err,
		)
	}
	if record.SchemaVersion != metadataSchemaVersion ||
		record.Reference != request.reference ||
		!reflect.DeepEqual(record.Platform, request.platform) ||
		record.Object == "" ||
		!filepath.IsLocal(record.Object) {
		return referenceRecord{}, false, fmt.Errorf(
			"OCI reference metadata for %q is invalid",
			request.reference,
		)
	}

	return record, true, nil
}

func encodeMetadata(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maximumMetadataBytes {
		return nil, fmt.Errorf(
			"metadata exceeds %d bytes",
			maximumMetadataBytes,
		)
	}

	return append(data, '\n'), nil
}

func decodeMetadata(data []byte, destination any) error {
	if len(data) > maximumMetadataBytes {
		return fmt.Errorf("metadata exceeds %d bytes", maximumMetadataBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var trailing any
	err := decoder.Decode(&trailing)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("metadata contains multiple JSON values")
		}
		return err
	}

	canonical, err := encodeMetadata(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, canonical) {
		return fmt.Errorf("metadata is not in canonical form")
	}

	return nil
}
