package sessionservice

// Embeds native MCP lifecycle and introspection documentation resources.

import "embed"

//go:embed resources/*.md
var resourceDocs embed.FS
