package runtimeassets

// Defines runtime-asset registrations and the optional concrete-tool
// contribution contract.

import (
	"io/fs"
)

// Asset is one transient byte asset mounted at Target for a sandbox run.
type Asset struct {
	Target string
	Data   []byte
	Mode   fs.FileMode
}

// Contributor is implemented by concrete built-in tools that need transient
// installers or wrappers mounted beneath /run/toby.
type Contributor interface {
	// RuntimeAssets returns the runtime assets contributed by the tool.
	RuntimeAssets() ([]Asset, error)
}
