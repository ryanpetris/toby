package socketrelay

// Validates, clones, and deterministically orders socket-relay registrations.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// Registry is an immutable collection of validated socket-relay requests.
type Registry struct {
	requests []Request
}

// NewRegistry validates the complete registration set before retaining a
// detached, target-sorted copy.
func NewRegistry(requests []Request) (*Registry, error) {
	normalized := append([]Request(nil), requests...)
	for index, request := range normalized {
		if err := validateRequest(request); err != nil {
			return nil, fmt.Errorf("socket relay %d: %w", index, err)
		}
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].SandboxSocket == normalized[j].SandboxSocket {
			return normalized[i].HostSocket < normalized[j].HostSocket
		}
		return normalized[i].SandboxSocket < normalized[j].SandboxSocket
	})
	for index, request := range normalized {
		for earlier := range index {
			previous := normalized[earlier]
			if mount.TargetsOverlap(
				previous.SandboxSocket,
				request.SandboxSocket,
			) {
				return nil, fmt.Errorf(
					"overlapping socket relay targets %q and %q",
					previous.SandboxSocket,
					request.SandboxSocket,
				)
			}
		}
	}

	return &Registry{requests: normalized}, nil
}

func validateRequest(request Request) error {
	if request.HostSocket == "" ||
		!filepath.IsAbs(request.HostSocket) ||
		filepath.Clean(request.HostSocket) != request.HostSocket ||
		strings.ContainsRune(request.HostSocket, 0) {
		return fmt.Errorf(
			"host socket must be a clean absolute path: %q",
			request.HostSocket,
		)
	}
	if err := mount.ValidateTarget(request.SandboxSocket); err != nil {
		return fmt.Errorf(
			"invalid sandbox socket %q: %w",
			request.SandboxSocket,
			err,
		)
	}
	if !strings.HasPrefix(
		request.SandboxSocket,
		layout.Runtime+"/",
	) {
		return fmt.Errorf(
			"sandbox socket %q must be strictly beneath %s",
			request.SandboxSocket,
			layout.Runtime,
		)
	}

	return nil
}
