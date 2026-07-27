package t3

// Embeds the T3 wrapper published into the native runtime directory.

import "embed"

//go:embed resources/t3-wrapper.sh
var t3Files embed.FS
