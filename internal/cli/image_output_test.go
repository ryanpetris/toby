package cli

// Covers stable redirected image output and inspection serialization.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"gopkg.in/yaml.v3"

	"petris.dev/toby/internal/oci"
)

func TestImageListOmitsNativePaths(t *testing.T) {
	image := testImageInfo()
	var output bytes.Buffer
	if err := writeImageList(&output, []oci.ImageInfo{image}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ID",
		"REFERENCE",
		"PLATFORM",
		"IMAGE ID",
		image.ShortID(),
		image.Reference,
		"linux/amd64",
		image.ImageID(),
		"ready",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("image list %q does not contain %q", output.String(), want)
		}
	}
	for _, path := range []string{
		image.RootfsPath,
		image.ObjectPath,
		image.MetadataPath,
	} {
		if strings.Contains(output.String(), path) {
			t.Fatalf("image list unexpectedly contains path %q", path)
		}
	}
}

func TestImageInspectionSupportsYAMLAndJSON(t *testing.T) {
	image := testImageInfo()

	var yamlOutput bytes.Buffer
	if err := writeImageInspection(&yamlOutput, image, "yaml"); err != nil {
		t.Fatal(err)
	}
	var yamlInspection imageInspection
	if err := yaml.Unmarshal(yamlOutput.Bytes(), &yamlInspection); err != nil {
		t.Fatal(err)
	}

	var jsonOutput bytes.Buffer
	if err := writeImageInspection(&jsonOutput, image, "json"); err != nil {
		t.Fatal(err)
	}
	var jsonInspection imageInspection
	if err := json.Unmarshal(jsonOutput.Bytes(), &jsonInspection); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(jsonInspection, yamlInspection) {
		t.Fatalf(
			"JSON inspection = %#v, want YAML inspection %#v",
			jsonInspection,
			yamlInspection,
		)
	}
	if yamlInspection.ID != image.ID ||
		yamlInspection.Reference != image.Reference ||
		yamlInspection.ImageDigest != image.Manifest.Digest.String() ||
		yamlInspection.Path != image.RootfsPath ||
		yamlInspection.Status != "ready" {
		t.Fatalf("inspection = %#v", yamlInspection)
	}
}

func testImageInfo() oci.ImageInfo {
	return oci.ImageInfo{
		ID:        strings.Repeat("a", 64),
		Kind:      oci.ImageEntryReference,
		Reference: "docker.io/library/alpine:latest",
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: "amd64",
		},
		Manifest: ocispec.Descriptor{
			Digest: digest.SHA256.FromString("manifest"),
		},
		Config: ocispec.Descriptor{
			Digest: digest.SHA256.FromString("config"),
		},
		References:    []string{"docker.io/library/alpine:latest"},
		ObjectKey:     "platform/sha256/digest",
		ReferencePath: "/data/images/references/reference.json",
		ObjectPath:    "/data/images/objects/platform/sha256/digest",
		MetadataPath:  "/data/images/objects/platform/sha256/digest/metadata.json",
		RootfsPath:    "/data/images/objects/platform/sha256/digest/bundle/rootfs",
		Runtime: oci.RuntimeConfig{
			Environment: []string{"PATH=/bin"},
			Workdir:     "/workspace",
			Command:     []string{"/bin/sh"},
			Labels:      map[string]string{"example": "value"},
		},
	}
}
