package githubcli

// The embedded GitHub CLI installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var githubCLIFiles embed.FS
