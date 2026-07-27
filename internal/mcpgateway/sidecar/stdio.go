package sidecar

// Adapts the concrete sidecar provider to one-process-per-connector local stdio
// MCP execution.

import (
	"context"
	"fmt"
	"time"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localstdio"
)

const defaultTerminationGrace = 2 * time.Second

// StdioLauncher launches and reaps one sidecar for each admitted connector.
type StdioLauncher struct {
	provider Provider
	grace    time.Duration
	logger   *diagnostic.Logger
}

var _ localstdio.Launcher = (*StdioLauncher)(nil)

// NewStdioLauncher constructs a concrete local stdio launcher.
func NewStdioLauncher(
	provider Provider,
	terminationGrace time.Duration,
	logger *diagnostic.Logger,
) (*StdioLauncher, error) {
	if isNilContract(provider) {
		return nil, fmt.Errorf("stdio sidecar provider is required")
	}
	if terminationGrace == 0 {
		terminationGrace = defaultTerminationGrace
	}
	if terminationGrace < 0 {
		return nil, fmt.Errorf(
			"stdio sidecar termination grace must not be negative",
		)
	}

	return &StdioLauncher{
		provider: provider,
		grace:    terminationGrace,
		logger:   logger,
	}, nil
}

// Prepare resolves the image to an immutable reference and pins every mount
// before the run gateway is published.
func (l *StdioLauncher) Prepare(
	ctx context.Context,
	launch localstdio.Launch,
	progress mcpgateway.ProgressReporter,
) (
	result localstdio.PreparedLaunch,
	returnErr error,
) {
	if l == nil || l.provider == nil {
		return nil, fmt.Errorf(
			"stdio sidecar launcher is not configured",
		)
	}
	if ctx == nil {
		return nil, fmt.Errorf(
			"prepare stdio sidecar context is nil",
		)
	}

	definition := Definition{
		Image:       launch.Image,
		Command:     append([]string(nil), launch.Command...),
		Environment: cloneEnvironment(launch.Environment),
		Mounts: append(
			[]mcpgateway.Mount(nil),
			launch.Mounts...,
		),
		Network: launch.Network,
	}
	mounts, err := l.provider.PinMounts(ctx, definition.Mounts)
	if err != nil {
		return nil, fmt.Errorf(
			"pin stdio sidecar mounts: %w",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			l.logger.DebugError(
				"close stdio sidecar mounts after preparation failure",
				mounts.Close(),
			)
		}
	}()

	metadata, err := l.provider.Resolve(ctx, definition, progress)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve stdio sidecar image: %w",
			err,
		)
	}
	definition.Image = metadata.ImmutableImage

	return &stdioPrepared{
		provider:   l.provider,
		grace:      l.grace,
		definition: definition,
		metadata:   metadata,
		mounts:     mounts,
		logger:     l.logger,
	}, nil
}

func cloneEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}

	return result
}
