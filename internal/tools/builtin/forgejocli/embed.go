package forgejocli

// Embeds the Forgejo CLI installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var forgejoCLIFiles embed.FS
