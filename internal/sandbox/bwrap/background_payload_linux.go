//go:build linux

package bwrap

// Retains the exact initial payload below Bubblewrap's PID-namespace reaper.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const backgroundPayloadPollInterval = 10 * time.Millisecond

func retainBackgroundPayload(
	ctx context.Context,
	init *processIdentity,
) (*processIdentity, error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"retain background Bubblewrap payload: context is nil",
		)
	}
	if init == nil {
		return nil, fmt.Errorf(
			"retain background Bubblewrap payload: init identity is nil",
		)
	}

	ticker := time.NewTicker(backgroundPayloadPollInterval)
	defer ticker.Stop()
	for {
		exited, err := init.Exited()
		if err != nil {
			return nil, err
		}
		if exited {
			return nil, fmt.Errorf(
				"bubblewrap init %d exited before launching its payload",
				init.pid,
			)
		}

		children, err := processChildPIDs(init.pid)
		if err != nil {
			return nil, fmt.Errorf(
				"inspect Bubblewrap init %d children: %w",
				init.pid,
				err,
			)
		}
		switch len(children) {
		case 0:
		case 1:
			payload, err := openProcessIdentity(
				children[0],
				init.pid,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"retain exact Bubblewrap payload: %w",
					err,
				)
			}

			return payload, nil
		default:
			return nil, fmt.Errorf(
				"bubblewrap init %d has %d direct children, want one initial payload",
				init.pid,
				len(children),
			)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf(
				"wait for Bubblewrap payload: %w",
				ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func processChildPIDs(pid int) ([]int, error) {
	data, err := os.ReadFile(filepath.Join(
		"/proc",
		strconv.Itoa(pid),
		"task",
		strconv.Itoa(pid),
		"children",
	))
	if err != nil {
		return nil, err
	}

	fields := strings.Fields(string(data))
	children := make([]int, 0, len(fields))
	seen := make(map[int]struct{}, len(fields))
	for _, field := range fields {
		child, err := strconv.Atoi(field)
		if err != nil || child <= 0 {
			return nil, fmt.Errorf(
				"process %d has invalid child PID %q",
				pid,
				field,
			)
		}
		if _, duplicate := seen[child]; duplicate {
			return nil, fmt.Errorf(
				"process %d repeats child PID %d",
				pid,
				child,
			)
		}
		seen[child] = struct{}{}
		children = append(children, child)
	}

	return children, nil
}
