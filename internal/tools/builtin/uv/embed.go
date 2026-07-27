package uv

// Embeds the uv installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var uvFiles embed.FS
