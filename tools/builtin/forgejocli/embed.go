package forgejocli

// The embedded Forgejo CLI installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var forgejoCLIFiles embed.FS
