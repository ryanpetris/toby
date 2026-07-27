package run

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/agent-overlap-check.sh
var agentOverlapCheckScriptFixture string

//go:embed testdata/agent-namespace-record.sh
var agentNamespaceRecordScriptFixture string
