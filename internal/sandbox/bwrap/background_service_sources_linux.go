//go:build linux

package bwrap

// Validates the exact descriptor set used to render an agent-owned background
// service.

import (
	"fmt"
	"os"

	"petris.dev/toby/internal/sandbox/mount"
)

// BackgroundServiceSources contains the authoritative caller-owned
// capabilities for every host object in a BackgroundServicePlan.
type BackgroundServiceSources struct {
	RootFS  *os.File
	Binds   map[string]*os.File
	Runtime *os.File
}

func validateBackgroundServiceSourceCardinality(
	plan BackgroundServicePlan,
	sources BackgroundServiceSources,
) error {
	expected := 1 + len(plan.Binds)
	if plan.Runtime != nil {
		expected++
	}
	provided := 1 + len(sources.Binds)
	if sources.Runtime != nil {
		provided++
	}

	if expected > maxBackgroundServiceSourceDescriptors ||
		provided > maxBackgroundServiceSourceDescriptors {
		return fmt.Errorf(
			"background-service descriptor count exceeds %d",
			maxBackgroundServiceSourceDescriptors,
		)
	}
	if expected != provided {
		return fmt.Errorf(
			"background-service descriptor count is %d, want %d",
			provided,
			expected,
		)
	}

	return nil
}

func validateBackgroundServiceSources(
	plan BackgroundServicePlan,
	sources BackgroundServiceSources,
) error {
	if err := validateBackgroundServiceSourceCardinality(
		plan,
		sources,
	); err != nil {
		return err
	}
	if err := validateDirectoryDescriptor(
		"background-service rootfs",
		sources.RootFS,
	); err != nil {
		return err
	}

	for _, bind := range plan.Binds {
		source, found := sources.Binds[bind.Target]
		if !found {
			return fmt.Errorf(
				"missing background-service bind source %q",
				bind.Target,
			)
		}
		if err := validateBackgroundServiceBindDescriptor(
			"background-service bind "+bind.Target,
			source,
			bind,
		); err != nil {
			return err
		}
	}
	if err := validateStringCoverage(
		"background-service bind",
		bindTargets(plan.Binds),
		sources.Binds,
	); err != nil {
		return err
	}

	switch {
	case plan.Runtime == nil && sources.Runtime != nil:
		return fmt.Errorf(
			"unexpected background-service runtime source",
		)
	case plan.Runtime != nil && sources.Runtime == nil:
		return fmt.Errorf("missing background-service runtime source")
	case plan.Runtime != nil:
		if err := validateDirectoryDescriptor(
			"background-service runtime",
			sources.Runtime,
		); err != nil {
			return err
		}
	}

	return validateDistinctBackgroundServiceDescriptors(plan, sources)
}

func validateBackgroundServiceBindDescriptor(
	label string,
	file *os.File,
	bind mount.Bind,
) error {
	if bind.Target == BackgroundServiceAuthSocketTarget {
		return validateBindDescriptor(label, file, mount.AccessDev)
	}

	info, err := descriptorInfo(label, file)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf(
			"%s read-only source must be a directory or regular-file descriptor, got %s",
			label,
			descriptorKind(info.Mode()),
		)
	}

	return nil
}

func validateDistinctBackgroundServiceDescriptors(
	plan BackgroundServicePlan,
	sources BackgroundServiceSources,
) error {
	descriptors := make(
		[]namedDescriptor,
		0,
		1+len(plan.Binds)+1,
	)
	descriptors = append(descriptors, namedDescriptor{
		label: "background-service rootfs",
		file:  sources.RootFS,
	})
	for _, bind := range plan.Binds {
		descriptors = append(descriptors, namedDescriptor{
			label: "background-service bind " + bind.Target,
			file:  sources.Binds[bind.Target],
		})
	}
	if plan.Runtime != nil {
		descriptors = append(descriptors, namedDescriptor{
			label: "background-service runtime",
			file:  sources.Runtime,
		})
	}

	identities := make([]descriptorIdentity, 0, len(descriptors))
	for _, descriptor := range descriptors {
		info, err := descriptor.file.Stat()
		if err != nil {
			return fmt.Errorf(
				"inspect %s source descriptor identity: %w",
				descriptor.label,
				err,
			)
		}
		for _, identity := range identities {
			if os.SameFile(info, identity.info) {
				return fmt.Errorf(
					"%s and %s alias the same host object",
					descriptor.label,
					identity.label,
				)
			}
		}
		identities = append(identities, descriptorIdentity{
			label: descriptor.label,
			info:  info,
		})
	}

	return nil
}
