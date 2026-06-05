// Package sandbox defines the contract a tool uses to act on the sandbox it runs
// in: filesystem and environment mutations, mounts, command execution, and the
// directory layout. It is the tool-facing surface of the sandbox runtime — the
// concrete runtime (e.g. the docker implementation) satisfies Service, and tools
// receive a Service at construction the way a provider receives an *http.Client.
package sandbox

import (
	"context"

	"petris.dev/toby/container/mount"
)

// ExecOptions tunes how Service.Exec runs a command in the sandbox.
type ExecOptions struct {
	HideOutput bool
	Foreground bool
	Root       bool
}

// Service is the set of operations a tool performs against its sandbox while
// preparing and running. The same Service spans the host build phase (declaring
// mounts, seeding files and environment) and the run phase (executing commands).
type Service interface {
	ProjectPath(string) (string, bool)
	VisibleHostPath(string) (string, error)
	GetEnvironment(string) (string, bool)
	SetEnvironment(context.Context, string, string) error
	PrependEnvironment(context.Context, string, string, string) error
	AppendEnvironment(context.Context, string, string, string) error
	AddBind(mount.Bind) error
	AddMount(mount.Request) (mount.Entry, error)
	Mount(mount.Key) (mount.Entry, bool)
	AddFile(context.Context, string, []byte, uint32) error
	AddFileOwned(context.Context, string, []byte, uint32, int, int) error
	DeletePath(context.Context, string, bool) error
	Mkdir(context.Context, string, uint32) error
	MkdirOwned(context.Context, string, uint32, int, int) error
	Symlink(context.Context, string, string) error
	SymlinkOwned(context.Context, string, string, int, int) error
	Exec(context.Context, []string, ExecOptions) (int, error)
	TobyMCPURL() string
}

// Paths reports the well-known directories inside a running sandbox.
type Paths interface {
	HomeDir() string
	Projects() string
	TobyRuntimeDir() string
	TobyContextDir() string
	TobyOpenCodeConfigDir() string
}
