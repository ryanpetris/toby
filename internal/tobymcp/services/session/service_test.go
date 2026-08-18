package sessionservice

// Verifies native snapshot introspection and structural secret exclusion.

import (
	"context"
	"strings"
	"testing"

	"petris.dev/toby/internal/tobymcp"
)

func TestDynamicRuntimeResourceIncludesVersion(t *testing.T) {
	session := &tobymcp.Session{Snapshot: testSessionSnapshot()}

	text, err := handler{session}.runtimeResource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"version"`) {
		t.Fatalf("runtime resource missing version: %s", text)
	}
}

func TestResourcesReadReturnsRequestedAndReportsUnknown(t *testing.T) {
	session := &tobymcp.Session{
		Snapshot: testSessionSnapshot(),
		Resources: []tobymcp.Resource{
			{
				URI:      "toby://docs/mcps",
				Name:     "toby.docs.mcps",
				Title:    "Toby-Managed MCPs",
				FS:       resourceDocs,
				FilePath: "resources/mcps.md",
			},
			{
				URI:   "toby://session/runtime",
				Name:  "toby.session.runtime",
				Title: "Toby Session Runtime",
				Text: func(
					ctx context.Context,
					session *tobymcp.Session,
				) (string, error) {
					return handler{session}.runtimeResource(ctx)
				},
			},
		},
	}

	result, output, err := handler{session}.resourcesRead(
		t.Context(),
		nil,
		ResourcesReadInput{URIs: []string{
			"toby://session/runtime",
			"toby://does/not/exist",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("unknown URI result = %#v, want tool error", result)
	}
	if len(output.Resources) != 2 {
		t.Fatalf("resources = %#v", output.Resources)
	}
	if output.Resources[0].URI != "toby://session/runtime" ||
		!strings.Contains(output.Resources[0].Text, `"version"`) {
		t.Fatalf("runtime read = %#v", output.Resources[0])
	}
	if output.Resources[1].Error == "" {
		t.Fatalf("unknown URI = %#v, want item error", output.Resources[1])
	}

	_, all, err := handler{session}.resourcesRead(
		t.Context(),
		nil,
		ResourcesReadInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Resources) != len(session.Resources) {
		t.Fatalf("read-all resources = %#v", all.Resources)
	}
	for _, resource := range all.Resources {
		if resource.Text == "" || resource.Error != "" {
			t.Fatalf("read-all entry = %#v", resource)
		}
	}
}

func TestIntrospectionResourcesRenderOnlyNativeSnapshot(t *testing.T) {
	snapshot := testSessionSnapshot()
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	session := &tobymcp.Session{Snapshot: snapshot}
	resourceHandler := handler{session}

	runtimeText, err := resourceHandler.runtimeResource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	toolsText, err := resourceHandler.toolsResource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	projectsText, err := resourceHandler.projectsResource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mcpsText, err := resourceHandler.mcpsResource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	text := runtimeText + toolsText + projectsText + mcpsText

	for _, expected := range []string{
		`"debug": true`,
		`"name": "native-work"`,
		`"profile": "default"`,
		`"runtime": "bubblewrap"`,
		`"rootfsDigest": "sha256:aaaaaaaa`,
		`"primary": "codex"`,
		`"sandboxPath": "/toby/workspace/project"`,
		`"key": "npm:cache"`,
		`"target": "/run/service.sock"`,
		`"name": "native-models"`,
		`"name": "native-mcp"`,
		`"network": "private"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf(
				"native snapshot resource missing %q: %s",
				expected,
				text,
			)
		}
	}
	for _, forbidden := range []string{
		`"host":`,
		`"hostPath":`,
		`"runtimeInfo":`,
		`"command":`,
		`"url":`,
		`"headers":`,
		`"argv":`,
		`"environment":`,
		`"credentials":`,
		`"pid":`,
		`"exitCode":`,
		`"updatedAt":`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf(
				"native snapshot resource exposed forbidden field %q: %s",
				forbidden,
				text,
			)
		}
	}
}

func TestServiceExposesResourcesReadTool(t *testing.T) {
	tools := (Service{}).Tools()
	if len(tools) != 1 || tools[0].Name != "resources_read" {
		t.Fatalf("session tools = %#v", tools)
	}
}

func testSessionSnapshot() tobymcp.SessionSnapshot {
	return tobymcp.SessionSnapshot{
		Debug: true,
		Runtime: tobymcp.SessionRuntime{
			Name:         "native-work",
			Profile:      "default",
			Runtime:      "bubblewrap",
			RootFSDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Network:      "host",
			Home:         "/toby/home",
			Workspace:    "/toby/workspace",
			Root:         "/toby",
			Bin:          "/toby/bin",
			Workdir:      "/toby/workspace/project",
		},
		Tools: tobymcp.SessionTools{
			Primary: "codex",
			Active:  []string{"codex"},
			Available: []tobymcp.SessionTool{{
				Name:          "codex",
				Launchable:    true,
				ContextGroups: []string{"agents"},
			}},
			Groups: map[string]string{"codex": "ai"},
		},
		Projects: []tobymcp.SessionProject{{
			Name:        "project",
			SandboxPath: "/toby/workspace/project",
		}},
		Mounts: []tobymcp.SessionMount{{
			Key:     "npm:cache",
			Profile: "default",
			Target:  "/toby/home/.npm",
			Access:  "regular",
		}},
		Binds: []tobymcp.SessionBind{{
			Target:   "/run/service.sock",
			Access:   "regular",
			Optional: true,
		}},
		Models: []tobymcp.SessionModelsEndpoint{{
			Name:   "native-models",
			Type:   "openai",
			Models: []string{"gpt-5"},
		}},
		MCPs: []tobymcp.SessionMCP{
			{
				Name:      "toby",
				Type:      "builtin",
				Enabled:   true,
				Status:    tobymcp.SessionMCPStatusReady,
				Transport: "stdio",
				Scope:     "run",
			},
			{
				Name:      "native-mcp",
				Type:      "local",
				Enabled:   true,
				Status:    tobymcp.SessionMCPStatusRunning,
				Runtime:   "bubblewrap",
				Transport: "http",
				Scope:     "home",
				Network:   "private",
			},
		},
	}
}
