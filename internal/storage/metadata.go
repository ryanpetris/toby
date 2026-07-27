package storage

// Defines canonical, versioned volume metadata and derives its stable
// BLAKE2b-512 directory identity.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/blake2b"
)

const storageSchemaVersion = 1

type volumeMetadata struct {
	SchemaVersion int        `json:"schema_version"`
	Type          VolumeType `json:"type"`
	Name          string     `json:"name"`
	Profile       string     `json:"profile,omitempty"`
	Purpose       string     `json:"purpose,omitempty"`
}

func newHomeMetadata(profile, name string) volumeMetadata {
	return volumeMetadata{
		SchemaVersion: storageSchemaVersion,
		Type:          VolumeTypeHome,
		Name:          name,
		Profile:       profile,
	}
}

func newToolMetadata(profile, name, purpose string) volumeMetadata {
	return volumeMetadata{
		SchemaVersion: storageSchemaVersion,
		Type:          VolumeTypeTool,
		Name:          name,
		Profile:       profile,
		Purpose:       purpose,
	}
}

func (m volumeMetadata) validate() error {
	if m.SchemaVersion != storageSchemaVersion {
		return fmt.Errorf("schema version is %d, want %d", m.SchemaVersion, storageSchemaVersion)
	}
	if !utf8.ValidString(m.Name) || m.Name == "" {
		return errors.New("name must be nonempty valid UTF-8")
	}

	switch m.Type {
	case VolumeTypeHome:
		if !utf8.ValidString(m.Profile) || m.Profile == "" {
			return errors.New("home volume profile must be nonempty valid UTF-8")
		}
		if m.Purpose != "" {
			return errors.New("home volume must not have a purpose")
		}
	case VolumeTypeTool:
		if !utf8.ValidString(m.Profile) || m.Profile == "" {
			return errors.New("tool volume profile must be nonempty valid UTF-8")
		}
		if !utf8.ValidString(m.Purpose) || m.Purpose == "" {
			return errors.New("tool volume purpose must be nonempty valid UTF-8")
		}
	default:
		return fmt.Errorf("unsupported volume type %q", m.Type)
	}

	return nil
}

func volumeID(metadata volumeMetadata, limit int64) (string, []byte, error) {
	if err := metadata.validate(); err != nil {
		return "", nil, err
	}

	data, err := encodeMetadata(metadata, limit)
	if err != nil {
		return "", nil, err
	}
	sum := blake2b.Sum512(data)

	return hex.EncodeToString(sum[:]), data, nil
}

func encodeMetadata(value any, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("metadata limit must be positive")
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("metadata exceeds %d bytes", limit)
	}
	return data, nil
}

func decodeMetadata(data []byte, limit int64, destination any) error {
	if limit <= 0 {
		return errors.New("metadata limit must be positive")
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("metadata exceeds %d bytes", limit)
	}
	if !utf8.Valid(data) {
		return errors.New("metadata is not valid UTF-8")
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
			return errors.New("metadata contains multiple JSON values")
		}
		return err
	}

	canonical, err := encodeMetadata(destination, limit)
	if err != nil {
		return fmt.Errorf("encode canonical metadata: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return errors.New("metadata is not canonically encoded")
	}

	return nil
}
