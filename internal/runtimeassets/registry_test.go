package runtimeassets

// Exercises registration validation and deterministic normalization before
// filesystem mutation.

import (
	"io/fs"
	"strings"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestNewRegistryRejectsInvalidAssets(t *testing.T) {
	t.Parallel()

	valid := Asset{
		Target: layout.Runtime + "/install.sh",
		Data:   []byte("#!/bin/sh\n"),
		Mode:   0o500,
	}
	tests := []struct {
		name   string
		assets []Asset
		want   string
	}{
		{
			name: "runtime root",
			assets: []Asset{{
				Target: layout.Runtime,
				Mode:   0o400,
			}},
			want: "strictly beneath",
		},
		{
			name: "outside runtime",
			assets: []Asset{{
				Target: "/tmp/install.sh",
				Mode:   0o400,
			}},
			want: "strictly beneath",
		},
		{
			name: "relative target",
			assets: []Asset{{
				Target: "install.sh",
				Mode:   0o400,
			}},
			want: "absolute",
		},
		{
			name: "unclean target",
			assets: []Asset{{
				Target: layout.Runtime + "/dir/../install.sh",
				Mode:   0o400,
			}},
			want: "clean",
		},
		{
			name: "non-permission mode",
			assets: []Asset{{
				Target: layout.Runtime + "/install.sh",
				Mode:   fs.ModeSymlink | 0o400,
			}},
			want: "non-permission",
		},
		{
			name: "not owner readable",
			assets: []Asset{{
				Target: layout.Runtime + "/install.sh",
				Mode:   0o200,
			}},
			want: "owner-readable",
		},
		{
			name: "group writable",
			assets: []Asset{{
				Target: layout.Runtime + "/install.sh",
				Mode:   0o620,
			}},
			want: "group- or other-writable",
		},
		{
			name:   "duplicate target",
			assets: []Asset{valid, valid},
			want:   "duplicate",
		},
		{
			name: "overlapping targets",
			assets: []Asset{
				{Target: layout.Runtime + "/asset", Mode: 0o400},
				{Target: layout.Runtime + "/asset-other", Mode: 0o400},
				{Target: layout.Runtime + "/asset/child", Mode: 0o400},
			},
			want: "overlapping",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRegistry(test.assets)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRegistry() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
