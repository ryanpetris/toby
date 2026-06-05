package emdash

// The embedded emdash installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var emdashFiles embed.FS
