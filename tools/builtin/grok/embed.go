package grok

// The embedded grok installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var grokFiles embed.FS
