package grok

// Embeds the Grok installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var grokFiles embed.FS
