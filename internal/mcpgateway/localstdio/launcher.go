package localstdio

// Defines the sidecar-launch boundary that must preserve one connector's raw
// bytes, drain diagnostic stderr separately, and reap the complete process.

import (
	"context"
	"fmt"
	"io"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
)

// Launcher resolves and pins one target definition during gateway acquisition
// without starting its per-connector process.
type Launcher interface {
	// Prepare resolves and pins a stdio launch definition.
	Prepare(
		context.Context,
		Launch,
		mcpgateway.ProgressReporter,
	) (PreparedLaunch, error)
}

// PreparedLaunch retains one immutable image and exact mount set. Serve starts
// a fresh process for one connector; Close releases retained capabilities only
// after every Serve has returned.
type PreparedLaunch interface {
	// Serve launches and relays one stdio MCP process.
	Serve(context.Context, io.ReadWriteCloser) error
	// Close releases retained launch capabilities.
	Close() error
}

// Launch is a detached, secret-bearing sidecar definition. It must never be
// logged or included in a sandbox descriptor.
type Launch struct {
	Image       string
	Command     []string
	Environment map[string]string
	Mounts      []mcpgateway.Mount
	Network     resource.Network
}

var _ fmt.Stringer = Launch{}

func launchFromSpec(spec mcpgateway.TargetSpec) Launch {
	environment := make(map[string]string, len(spec.Environment))
	for name, value := range spec.Environment {
		environment[name] = value
	}

	return Launch{
		Image:       spec.Image,
		Command:     append([]string(nil), spec.Command...),
		Environment: environment,
		Mounts:      append([]mcpgateway.Mount(nil), spec.Mounts...),
		Network:     spec.Network,
	}
}

// String redacts every process, mount, environment, and image detail.
func (Launch) String() string {
	return "{Image:<redacted> Command:<redacted> Environment:<redacted> Mounts:<redacted> Network:<redacted>}"
}

func (l Launch) clone() Launch {
	clone := l
	clone.Command = append([]string(nil), l.Command...)
	clone.Mounts = append([]mcpgateway.Mount(nil), l.Mounts...)
	clone.Environment = make(map[string]string, len(l.Environment))
	for name, value := range l.Environment {
		clone.Environment[name] = value
	}

	return clone
}
