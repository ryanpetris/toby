// Package layout defines the stable filesystem paths exposed inside a Toby
// Bubblewrap sandbox.
package layout

import (
	"path"
	"strings"
)

const (
	// Root is the root of Toby-owned paths inside a sandbox.
	Root = "/toby"
	// Home is the sandbox user's private home.
	Home = "/toby/home"
	// Workspace is the root of project mounts.
	Workspace = "/toby/workspace"
	// Bin contains Toby-provided executables.
	Bin = "/toby/bin"
	// Runtime contains run-scoped capabilities.
	Runtime = "/run/toby"
)

// SandboxBinary returns the mounted sandbox-helper binary path.
func SandboxBinary() string {
	return path.Join(Bin, "tobys")
}

// SandboxSocket returns the fixed path at which a sandbox reaches its
// run-scoped launch client.
func SandboxSocket() string {
	return path.Join(Runtime, "sandbox.sock")
}

// ExpandHome expands a leading tilde to the sandbox home.
func ExpandHome(value string) string {
	switch {
	case value == "~":
		return Home
	case strings.HasPrefix(value, "~/"):
		return Home + value[1:]
	default:
		return value
	}
}
