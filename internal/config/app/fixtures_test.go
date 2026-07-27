package appconfig

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/merged-base.json
var mergedBaseJSONFixture string

//go:embed testdata/merged-override.yaml
var mergedOverrideYAMLFixture string

//go:embed testdata/mcp-resources.yaml
var mcpResourcesYAMLFixture string

//go:embed testdata/missing-mcp-environment.yaml
var missingMCPEnvironmentYAMLFixture string

//go:embed testdata/effective-settings.yaml
var effectiveSettingsYAMLFixture string

//go:embed testdata/tool-profiles.yaml
var toolProfilesYAMLFixture string

//go:embed testdata/instruction-glob.yaml
var instructionGlobYAMLFixture string
