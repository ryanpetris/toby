package tobymcp

// Embeds the instructions advertised by each Toby MCP server instance.

import _ "embed"

//go:embed resources/instructions.txt
var serverInstructions string
