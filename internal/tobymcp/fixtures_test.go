package tobymcp

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/valid-session-snapshot.json
var validSessionSnapshotFixture string

//go:embed testdata/invalid-session-prefix.json
var invalidSessionPrefixFixture string

//go:embed testdata/secret-models.json
var secretModelsFixture string

//go:embed testdata/secret-mcp-command.json
var secretMCPCommandFixture string

//go:embed testdata/secret-mcp-status.json
var secretMCPStatusFixture string

//go:embed testdata/secret-bind-source.json
var secretBindSourceFixture string
