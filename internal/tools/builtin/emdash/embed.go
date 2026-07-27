package emdash

// Embeds the emdash installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var emdashFiles embed.FS
