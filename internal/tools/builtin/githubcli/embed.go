package githubcli

// Embeds the GitHub CLI installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var githubCLIFiles embed.FS
