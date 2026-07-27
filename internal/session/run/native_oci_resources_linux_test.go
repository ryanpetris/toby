//go:build linux

package run

// Verifies launch identity propagation into deterministic local build
// references.

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/oci/imagesource"
)

func TestNativeOCIResourceRequestsNamesBuildForProfileAndProject(
	t *testing.T,
) {
	root := t.TempDir()
	requests, sandbox, err := nativeOCIResourceRequests(
		appconfig.SandboxConfig{
			Source: imagesource.Build,
			Build: imagesource.BuildConfig{
				Context:    root,
				Dockerfile: filepath.Join(root, "Dockerfile"),
			},
		},
		appconfig.ResourcesConfig{},
		"personal",
		"toby",
	)
	if err != nil {
		t.Fatal(err)
	}

	prefix := "toby.local/personal/toby:"
	if !strings.HasPrefix(sandbox.Reference, prefix) {
		t.Fatalf("sandbox reference = %q", sandbox.Reference)
	}
	if len(strings.TrimPrefix(sandbox.Reference, prefix)) != 64 {
		t.Fatalf("sandbox reference tag = %q", sandbox.Reference)
	}
	if sandbox.Platform.Architecture != runtime.GOARCH {
		t.Fatalf(
			"sandbox architecture = %q, want %q",
			sandbox.Platform.Architecture,
			runtime.GOARCH,
		)
	}
	if len(requests) != 1 ||
		requests[0].configuration.Reference != sandbox.Reference {
		t.Fatalf("requests = %#v", requests)
	}
}
