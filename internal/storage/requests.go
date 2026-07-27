package storage

// Normalizes tool-volume requests, coalesces exact duplicates, and rejects key
// or sandbox-target conflicts before filesystem mutation.

import (
	"errors"
	"fmt"
	"reflect"
	"sort"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

func normalizeRequests(requests []mount.Request, occupiedTargets []string) ([]mount.Request, error) {
	normalized := make([]mount.Request, 0, len(requests))
	byKey := make(map[mount.Key]mount.Request, len(requests))

	for index, request := range requests {
		if request.Access == "" {
			request.Access = mount.AccessRegular
		}
		request.Target = layout.ExpandHome(request.Target)
		if err := validateRequest(request); err != nil {
			return nil, fmt.Errorf("tool-volume request %d: %w", index, err)
		}

		if existing, ok := byKey[request.Key]; ok {
			if !reflect.DeepEqual(existing, request) {
				return nil, fmt.Errorf("%w for key %s", ErrConflictingRequest, request.Key)
			}
			continue
		}
		byKey[request.Key] = request
		normalized = append(normalized, request)
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Target != normalized[j].Target {
			return normalized[i].Target < normalized[j].Target
		}
		return normalized[i].Key.String() < normalized[j].Key.String()
	})

	targets := make([]string, 0, len(occupiedTargets)+len(normalized))
	for index, target := range occupiedTargets {
		target = layout.ExpandHome(target)
		if err := mount.ValidateTarget(target); err != nil {
			return nil, fmt.Errorf("occupied sandbox target %d: %w", index, err)
		}
		targets = append(targets, target)
	}
	for _, request := range normalized {
		for _, target := range targets {
			if mount.TargetsOverlap(request.Target, target) {
				return nil, fmt.Errorf(
					"tool-volume target %q overlaps %q",
					request.Target,
					target,
				)
			}
		}
		targets = append(targets, request.Target)
	}

	return normalized, nil
}

func validateRequest(request mount.Request) error {
	if err := request.Key.Validate(); err != nil {
		return err
	}
	if err := request.Access.Validate(); err != nil {
		return err
	}
	if err := request.Seed.Validate(); err != nil {
		return err
	}
	if request.Target == "" {
		return errors.New("tool-volume target must not be empty")
	}

	probe := mount.Entry{
		Key:      request.Key,
		Profile:  defaultProfile,
		HostPath: "/toby-validation-placeholder",
		Target:   request.Target,
		Access:   request.Access,
		Optional: request.Optional,
		Seed:     request.Seed,
	}
	return probe.Validate()
}
