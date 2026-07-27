package storage

// Covers canonical volume metadata, identities, strict decoding, and bounds.

import (
	"bytes"
	"strings"
	"testing"
)

func TestVolumeMetadataIdentityAndRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		metadata volumeMetadata
		id       string
	}{
		{
			name:     "home",
			metadata: newHomeMetadata("default", "workspace"),
			id:       "8bb7fb68b5ba0c140f14f7cade84449d447d20dba0d622b09e28431044d62f1b17afe45a9e8057defd50d1e7cd44c5da2690834d330b6565a60c6017470911f5",
		},
		{
			name: "tool",
			metadata: newToolMetadata(
				"default",
				"opencode",
				"config",
			),
			id: "07d2b7a602e6fc6a5fc5c2f78e01f74461bb1e065be930eaae69b6a0ef30bc3f77840fd1b561aa45a01a10546fd05087278a2514acf7a945cf6090c723e31e29",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, data, err := volumeID(
				test.metadata,
				DefaultLimits().MetadataSize,
			)
			if err != nil {
				t.Fatal(err)
			}
			if id != test.id {
				t.Fatalf("volume id = %q, want %q", id, test.id)
			}

			var decoded volumeMetadata
			if err := decodeMetadata(
				data,
				DefaultLimits().MetadataSize,
				&decoded,
			); err != nil {
				t.Fatal(err)
			}
			if decoded != test.metadata {
				t.Fatalf("decoded metadata = %#v, want %#v", decoded, test.metadata)
			}

			again, canonical, err := volumeID(
				decoded,
				DefaultLimits().MetadataSize,
			)
			if err != nil {
				t.Fatal(err)
			}
			if again != id || !bytes.Equal(canonical, data) {
				t.Fatal("volume identity is not stable across canonical round trip")
			}
		})
	}
}

func TestVolumeMetadataSeparatesIdentities(t *testing.T) {
	metadata := []volumeMetadata{
		newHomeMetadata("default", "one"),
		newHomeMetadata("default", "two"),
		newHomeMetadata("work", "one"),
		newToolMetadata("default", "opencode", "config"),
		newToolMetadata("work", "opencode", "config"),
		newToolMetadata("default", "opencode", "data"),
		newToolMetadata("default", "codex", "config"),
	}

	seen := map[string]bool{}
	for _, item := range metadata {
		id, _, err := volumeID(item, DefaultLimits().MetadataSize)
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate volume identity %q for %#v", id, item)
		}
		seen[id] = true
	}
}

func TestVolumeMetadataRejectsInvalidShapes(t *testing.T) {
	tests := []volumeMetadata{
		{},
		{SchemaVersion: 2, Type: VolumeTypeHome, Name: "home", Profile: "default"},
		{SchemaVersion: 1, Type: "unknown", Name: "name"},
		{SchemaVersion: 1, Type: VolumeTypeHome},
		{SchemaVersion: 1, Type: VolumeTypeHome, Name: "home"},
		{SchemaVersion: 1, Type: VolumeTypeHome, Name: "home", Profile: "default", Purpose: "state"},
		{SchemaVersion: 1, Type: VolumeTypeTool, Name: "opencode", Purpose: "state"},
		{SchemaVersion: 1, Type: VolumeTypeTool, Name: "opencode", Profile: "default"},
	}
	for _, metadata := range tests {
		if _, _, err := volumeID(metadata, DefaultLimits().MetadataSize); err == nil {
			t.Fatalf("invalid metadata was accepted: %#v", metadata)
		}
	}
}

func TestDecodeMetadataRejectsUnknownTrailingAndNonCanonical(t *testing.T) {
	valid, err := encodeMetadata(
		newHomeMetadata("default", "home"),
		DefaultLimits().MetadataSize,
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := [][]byte{
		[]byte(`{"schema_version":1,"type":"home","name":"home","profile":"default","extra":true}` + "\n"),
		append(append([]byte(nil), valid...), []byte("{}\n")...),
		[]byte(`{"type":"home","name":"home","profile":"default","schema_version":1}` + "\n"),
		[]byte("{\n  \"schema_version\": 1,\n  \"type\": \"home\",\n  \"name\": \"home\",\n  \"profile\": \"default\"\n}\n"),
		[]byte(`{"schema_version":1,"type":"home","name":"home","profile":"default"}`),
		[]byte(`{"schema_version":1,"type":"home","name":"\xff","profile":"default"}` + "\n"),
	}
	for _, data := range tests {
		var metadata volumeMetadata
		if err := decodeMetadata(
			data,
			DefaultLimits().MetadataSize,
			&metadata,
		); err == nil {
			t.Fatalf("invalid metadata was accepted: %q", data)
		}
	}
}

func TestMetadataBounds(t *testing.T) {
	metadata := newHomeMetadata("default", strings.Repeat("x", 100))
	if _, err := encodeMetadata(metadata, 1); err == nil {
		t.Fatal("oversized metadata was encoded")
	}

	data, err := encodeMetadata(metadata, DefaultLimits().MetadataSize)
	if err != nil {
		t.Fatal(err)
	}
	var decoded volumeMetadata
	if err := decodeMetadata(data, int64(len(data)-1), &decoded); err == nil {
		t.Fatal("oversized metadata was decoded")
	}
}
