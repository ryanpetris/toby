package t3

// The embedded t3 wrapper script written into the sandbox context directory.

import "embed"

//go:embed resources/t3-wrapper.sh
var t3Files embed.FS
