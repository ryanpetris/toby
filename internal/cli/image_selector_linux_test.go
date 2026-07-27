//go:build linux

package cli

// Covers image platform parsing and canonical pull request identities.

import (
	"runtime"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/oci/image"
)

func TestParseImagePlatform(t *testing.T) {
	current, err := parseImagePlatform("", true)
	if err != nil {
		t.Fatal(err)
	}
	if current.OS != "linux" ||
		current.Architecture != runtime.GOARCH {
		t.Fatalf("default platform = %#v", current)
	}

	arm, err := parseImagePlatform("linux/arm64/v8", false)
	if err != nil {
		t.Fatal(err)
	}
	if arm.OS != "linux" ||
		arm.Architecture != "arm64" ||
		arm.Variant != "v8" {
		t.Fatalf("explicit platform = %#v", arm)
	}

	for _, value := range []string{
		"amd64",
		"darwin/amd64",
		"linux//v8",
		"linux/amd64/extra/value",
	} {
		if _, err := parseImagePlatform(value, false); err == nil {
			t.Errorf("parseImagePlatform(%q) succeeded", value)
		}
	}
}

func TestNormalizeImagePullRequestsCanonicalizesAndDeduplicates(t *testing.T) {
	requests, err := normalizeImagePullRequests(
		[]string{
			"alpine",
			"docker.io/library/alpine:latest",
		},
		mustImagePlatform(t, "linux/amd64"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].Reference != "docker.io/library/alpine:latest" ||
		requests[0].PullPolicy != image.PullAlways {
		t.Fatalf("request = %#v", requests[0])
	}
}

func mustImagePlatform(t *testing.T, value string) ocispec.Platform {
	t.Helper()
	platform, err := parseImagePlatform(value, false)
	if err != nil {
		t.Fatal(err)
	}
	return platform
}
