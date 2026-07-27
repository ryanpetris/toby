package npm

// Embeds the npm initialization script published into the native runtime directory.

import "embed"

//go:embed resources/sandbox-init.sh
var npmFiles embed.FS
