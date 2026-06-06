package sessionservice

// The embedded Toby documentation resources served at toby://docs/mcps and
// toby://docs/introspection.

import "embed"

//go:embed resources/*.md
var resourceDocs embed.FS
