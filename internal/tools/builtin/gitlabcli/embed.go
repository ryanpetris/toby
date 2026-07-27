package gitlabcli

// Embeds the GitLab CLI installer published into the native runtime directory.

import "embed"

//go:embed resources/install.sh
var gitlabCLIFiles embed.FS
