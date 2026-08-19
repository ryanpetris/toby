//go:build linux

package run

// Verifies launch identity propagation into deterministic local build
// references.

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appconfig "petris.dev/toby/internal/config/app"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/providergateway/caddy"
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

func TestNativeOCIResourceRequestsIncludesMCPAndCaddy(t *testing.T) {
	requests, _, err := nativeOCIResourceRequests(
		appconfig.SandboxConfig{
			Source: imagesource.Registry,
			Image:  "ghcr.io/example/sandbox:latest",
			Pull:   "if-missing",
		},
		appconfig.ResourcesConfig{
			MCPs: map[string]mcpconfig.Server{
				"docs": {
					Type: mcpconfig.ServerRemote,
				},
				"sidecar": {
					Type:  mcpconfig.ServerLocal,
					Image: "ghcr.io/example/mcp:latest",
				},
			},
			Models: map[string]modelsconfig.Config{
				"openai": {URL: "https://example.invalid/v1"},
			},
		},
		"default",
		"toby",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].kind != ociResourceSandbox {
		t.Fatalf("sandbox kind = %d", requests[0].kind)
	}
	if requests[1].kind != ociResourceMCP ||
		requests[1].configuration.Reference != "ghcr.io/example/mcp:latest" ||
		len(requests[1].mcpNames) != 1 ||
		requests[1].mcpNames[0] != "sidecar" {
		t.Fatalf("mcp request = %#v", requests[1])
	}
	if requests[2].kind != ociResourceCaddy ||
		requests[2].configuration.Reference != caddy.DefaultImage {
		t.Fatalf("caddy request = %#v", requests[2])
	}
}

func TestApplyUnavailableOCIResourceSkipsOptionalImages(t *testing.T) {
	resources := &appconfig.ResourcesConfig{
		MCPs: map[string]mcpconfig.Server{
			"sidecar": {},
			"other":   {},
		},
		Models: map[string]modelsconfig.Config{
			"openai": {},
		},
	}
	logger := &ociWarningLogger{}
	warnings := warning.NewService(logger, nil)

	if err := applyUnavailableOCIResource(
		warnings,
		resources,
		ociPrepareResult{
			request: namedOCIResource{
				kind: ociResourceMCP,
				configuration: ociresourceConfig(
					"ghcr.io/example/mcp:latest",
				),
				mcpNames: []string{"sidecar"},
			},
			err: errors.New("pull failed"),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, ok := resources.MCPs["sidecar"]; ok {
		t.Fatal("unavailable MCP server was kept")
	}
	if _, ok := resources.MCPs["other"]; !ok {
		t.Fatal("unrelated MCP server was removed")
	}

	if err := applyUnavailableOCIResource(
		warnings,
		resources,
		ociPrepareResult{
			request: namedOCIResource{
				kind: ociResourceCaddy,
				configuration: ociresourceConfig(
					caddy.DefaultImage,
				),
			},
			err: errors.New("caddy pull failed"),
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(resources.Models) != 0 {
		t.Fatalf("models = %#v", resources.Models)
	}

	err := applyUnavailableOCIResource(
		warnings,
		resources,
		ociPrepareResult{
			request: namedOCIResource{
				kind: ociResourceSandbox,
				configuration: ociresourceConfig(
					"ghcr.io/example/sandbox:latest",
				),
			},
			err: errors.New("sandbox pull failed"),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "sandbox pull failed") {
		t.Fatalf("sandbox error = %v", err)
	}
	joined := strings.Join(logger.messages, "\n")
	if !strings.Contains(joined, string(warning.MCPImageUnavailable)) ||
		!strings.Contains(joined, string(warning.ModelsEndpointUnavailable)) {
		t.Fatalf("warnings = %q", joined)
	}
}

func ociresourceConfig(reference string) ociresource.Config {
	return ociresource.Config{Reference: reference}
}

type ociWarningLogger struct {
	messages []string
}

func (l *ociWarningLogger) Warn(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func (l *ociWarningLogger) WarnError(
	message string,
	_ error,
	_ ...any,
) {
	l.messages = append(l.messages, message)
}

func (l *ociWarningLogger) Error(message string, _ ...any) {
	l.messages = append(l.messages, message)
}
