package npm

// The embedded npm sandbox-init script written into the sandbox context directory.

import "embed"

//go:embed resources/sandbox-init.sh
var npmFiles embed.FS
