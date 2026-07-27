package mcpconfig

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/unknown-server-field.yaml
var mcpUnknownServerFieldFixture string

//go:embed testdata/unknown-endpoint-field.yaml
var mcpUnknownEndpointFieldFixture string

//go:embed testdata/invalid-command-type.yaml
var mcpInvalidCommandTypeFixture string

//go:embed testdata/remote-command.yaml
var mcpRemoteCommandFixture string

//go:embed testdata/stdio-endpoint.yaml
var mcpStdioEndpointFixture string
