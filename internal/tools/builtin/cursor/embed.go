package cursor

// Embeds the Cursor installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var cursorFiles embed.FS
