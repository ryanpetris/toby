package sidecar

// Retains exact configured mount inodes across reusable-process planning and
// every later generation start.

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/sandbox/mount"
)

// MountCapabilities owns one descriptor for each exact configured mount.
// Callers may prepare multiple sequential generations until Close revokes the
// retained capabilities.
type MountCapabilities struct {
	mu sync.Mutex

	definition []mcpgateway.Mount
	binds      []mount.Bind
	sources    map[string]*os.File
	logger     *diagnostic.Logger
	closed     bool
}

var (
	_ fmt.Stringer = (*MountCapabilities)(nil)
	_ io.Closer    = (*MountCapabilities)(nil)
)

// String withholds every host and sandbox path.
func (*MountCapabilities) String() string {
	return "{Mounts:<redacted>}"
}

// Identities returns the stable inode identity of each retained target without
// exposing its host source path.
func (m *MountCapabilities) Identities() (
	map[string]resource.MountSourceIdentity,
	error,
) {
	if m == nil {
		return nil, fmt.Errorf(
			"sidecar mount capabilities are required",
		)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf(
			"sidecar mount capabilities are closed",
		)
	}

	result := make(
		map[string]resource.MountSourceIdentity,
		len(m.sources),
	)
	for target, source := range m.sources {
		var status unix.Stat_t
		if err := unix.Fstat(int(source.Fd()), &status); err != nil {
			return nil, fmt.Errorf(
				"inspect sidecar mount capability: %w",
				err,
			)
		}
		result[target] = resource.MountSourceIdentity{
			Device:   uint64(status.Dev),
			Inode:    status.Ino,
			FileType: status.Mode & unix.S_IFMT,
		}
	}

	return result, nil
}

// Close releases every retained mount descriptor.
func (m *MountCapabilities) Close() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sources := m.sources
	m.sources = nil
	m.binds = nil
	m.definition = nil
	m.mu.Unlock()

	m.logger.DebugError(
		"close sidecar mount capabilities",
		closeFiles(sources),
	)
	return nil
}

func (m *MountCapabilities) duplicate(
	definition []mcpgateway.Mount,
) ([]mount.Bind, map[string]*os.File, error) {
	if m == nil {
		return nil, nil, fmt.Errorf(
			"sidecar mount capabilities are required",
		)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, fmt.Errorf(
			"sidecar mount capabilities are closed",
		)
	}
	if !sameMountDefinitions(m.definition, definition) {
		return nil, nil, fmt.Errorf(
			"sidecar mount capabilities do not match the launch definition",
		)
	}

	binds := append([]mount.Bind(nil), m.binds...)
	sources := make(map[string]*os.File, len(m.sources))
	for target, source := range m.sources {
		duplicate, err := duplicateFile(source, m.logger)
		if err != nil {
			m.logger.DebugError(
				"close duplicated sidecar mount capabilities",
				closeFiles(sources),
			)
			return nil, nil, fmt.Errorf(
				"duplicate sidecar mount capability: %w",
				err,
			)
		}
		sources[target] = duplicate
	}

	return binds, sources, nil
}

func sameMountDefinitions(
	first []mcpgateway.Mount,
	second []mcpgateway.Mount,
) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}

	return true
}
