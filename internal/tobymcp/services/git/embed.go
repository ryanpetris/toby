package gitservice

// The embedded git documentation resource served at toby://docs/git.

import "embed"

//go:embed resources/*.md
var resourceDocs embed.FS
