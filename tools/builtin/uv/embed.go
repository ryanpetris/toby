package uv

// The embedded uv installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var uvFiles embed.FS
