package caddy

// Verifies Caddy image normalization and the fixed in-image command contract.

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeImage(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", 64)
	tests := []struct {
		name      string
		image     string
		wantImage string
		wantError bool
	}{
		{
			name:      "default",
			wantImage: DefaultImage,
		},
		{
			name:      "mutable tag",
			image:     "caddy:latest",
			wantImage: DefaultImage,
		},
		{
			name:  "normalized digest",
			image: "example.test/team/caddy@sha256:" + digest,
			wantImage: "example.test/team/caddy@sha256:" +
				digest,
		},
		{
			name:  "Docker shorthand",
			image: "caddy@sha256:" + digest,
			wantImage: "docker.io/library/caddy@sha256:" +
				digest,
		},
		{
			name: "embedded digest text",
			image: "example.test/caddy:tag@sha256:" +
				"short",
			wantError: true,
		},
		{
			name: "URL syntax",
			image: "https://example.test/caddy@sha256:" +
				digest,
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeImage(test.image)
			if test.wantError {
				if err == nil {
					t.Fatalf(
						"normalized invalid image to %q",
						got,
					)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantImage {
				t.Fatalf(
					"image = %q, want %q",
					got,
					test.wantImage,
				)
			}
		})
	}
}

func TestDefaultCommandReadsNativeJSONFromStandardInput(t *testing.T) {
	t.Parallel()

	want := []string{
		defaultBinary,
		"run",
		"--config",
		"-",
	}
	if !slices.Equal(defaultCommand, want) {
		t.Fatalf(
			"default command = %q, want %q",
			defaultCommand,
			want,
		)
	}
}
