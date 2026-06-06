package gitlabcli

// The embedded GitLab CLI installer script written into the sandbox context directory.

import "embed"

//go:embed resources/install.sh
var gitlabCLIFiles embed.FS
