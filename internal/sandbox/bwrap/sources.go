package bwrap

// Defines the already-open filesystem capabilities consumed by the Bubblewrap
// renderer and validates their exact coverage and descriptor kinds.

import (
	"fmt"
	"io/fs"
	"os"
	"sort"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/mount"
)

const maxRenderedSourceDescriptors = 4096

// Sources contains caller-owned descriptors for every host object named by a
// Plan. Render duplicates these descriptors and never opens the Plan's
// diagnostic paths. The caller must keep the descriptors open until Render
// returns and remains responsible for closing them.
type Sources struct {
	ProtectedRoots     ProtectedRoots
	RootFS             *os.File
	OverlayUpper       *os.File
	OverlayWork        *os.File
	Home               *os.File
	ManagedDirectories map[mount.Key]*os.File
	Projects           map[string]*os.File
	Binds              map[string]*os.File
	BindParents        map[string]*os.File
	BindNames          map[string]string
	RuntimeAssets      map[string]*os.File
	SandboxBinary      *os.File
}

// ProtectedRoots contains the authoritative per-user storage roots that a
// project must never equal, contain, or be contained by.
type ProtectedRoots struct {
	ImageStore     *os.File
	PersistentData *os.File
	RunStorage     *os.File
	Runtime        *os.File
}

func validateSources(plan Plan, sources Sources) error {
	if err := validateSourceCardinality(plan, sources); err != nil {
		return err
	}

	for _, source := range []struct {
		label string
		file  *os.File
	}{
		{label: "OCI image-store root", file: sources.ProtectedRoots.ImageStore},
		{label: "Toby persistent-data root", file: sources.ProtectedRoots.PersistentData},
		{label: "Bubblewrap run-storage root", file: sources.ProtectedRoots.RunStorage},
		{label: "Toby runtime root", file: sources.ProtectedRoots.Runtime},
	} {
		if err := validateDirectoryDescriptor(source.label, source.file); err != nil {
			return err
		}
	}

	for _, source := range []struct {
		label string
		file  *os.File
	}{
		{label: "rootfs", file: sources.RootFS},
		{label: "overlay upper", file: sources.OverlayUpper},
		{label: "overlay work", file: sources.OverlayWork},
		{label: "private home", file: sources.Home},
	} {
		if err := validateDirectoryDescriptor(source.label, source.file); err != nil {
			return err
		}
	}

	for _, entry := range plan.ManagedDirectories {
		source, found := sources.ManagedDirectories[entry.Key]
		if !found {
			return fmt.Errorf(
				"missing managed-directory source %q",
				entry.Key,
			)
		}
		if entry.Access == mount.AccessDev {
			return fmt.Errorf(
				"managed-directory source %q cannot request device access",
				entry.Key,
			)
		}
		if err := validateDirectoryDescriptor(
			"managed-directory "+entry.Key.String(),
			source,
		); err != nil {
			return err
		}
	}
	if err := validateManagedCoverage(
		plan.ManagedDirectories,
		sources.ManagedDirectories,
	); err != nil {
		return err
	}

	for _, project := range plan.Projects {
		source, found := sources.Projects[project.Name]
		if !found {
			return fmt.Errorf("missing project source %q", project.Name)
		}
		if err := validateDirectoryDescriptor(
			"project "+project.Name,
			source,
		); err != nil {
			return err
		}
	}
	if err := validateStringCoverage(
		"project",
		projectNames(plan.Projects),
		sources.Projects,
	); err != nil {
		return err
	}
	if err := validateOwnedDescriptorIdentities(plan, sources); err != nil {
		return err
	}
	if err := validateProjectDescriptorIsolation(plan, sources); err != nil {
		return err
	}

	for _, bind := range plan.Binds {
		source, found := sources.Binds[bind.Target]
		if !found {
			return fmt.Errorf("missing external-bind source %q", bind.Target)
		}
		if err := validateBindDescriptor(
			"external bind "+bind.Target,
			source,
			bind.Access,
		); err != nil {
			return err
		}
		if err := validateExternalBindLinkShape(
			"external bind "+bind.Target,
			source,
		); err != nil {
			return err
		}

		parent, found := sources.BindParents[bind.Target]
		if !found {
			return fmt.Errorf(
				"missing external-bind parent source %q",
				bind.Target,
			)
		}
		if err := validateDirectoryDescriptor(
			"external bind "+bind.Target+" parent",
			parent,
		); err != nil {
			return err
		}
		name, found := sources.BindNames[bind.Target]
		if !found {
			return fmt.Errorf(
				"missing external-bind resolved name %q",
				bind.Target,
			)
		}
		if err := validateExternalBindParent(
			bind,
			source,
			parent,
			name,
		); err != nil {
			return err
		}
	}
	if err := validateStringCoverage(
		"external-bind",
		bindTargets(plan.Binds),
		sources.Binds,
	); err != nil {
		return err
	}
	if err := validateStringCoverage(
		"external-bind parent",
		bindTargets(plan.Binds),
		sources.BindParents,
	); err != nil {
		return err
	}
	if err := validateStringCoverage(
		"external-bind resolved name",
		bindTargets(plan.Binds),
		sources.BindNames,
	); err != nil {
		return err
	}
	if err := validateBindDescriptorIsolation(plan, sources); err != nil {
		return err
	}

	for _, asset := range plan.RuntimeAssets {
		source, found := sources.RuntimeAssets[asset.Target]
		if !found {
			return fmt.Errorf("missing runtime-asset source %q", asset.Target)
		}
		if err := validateBindDescriptor(
			"runtime asset "+asset.Target,
			source,
			asset.Access,
		); err != nil {
			return err
		}
	}
	if err := validateStringCoverage(
		"runtime-asset",
		runtimeAssetTargets(plan.RuntimeAssets),
		sources.RuntimeAssets,
	); err != nil {
		return err
	}

	if err := validateExecutableDescriptor("sandbox helper", sources.SandboxBinary); err != nil {
		return err
	}

	return nil
}

func validateSourceCardinality(plan Plan, sources Sources) error {
	wantDescriptors := 5 +
		len(plan.ManagedDirectories) +
		len(plan.Projects) +
		len(plan.Binds) +
		len(plan.RuntimeAssets)
	if wantDescriptors > maxRenderedSourceDescriptors {
		return fmt.Errorf(
			"bubblewrap plan requires %d source descriptors, limit is %d",
			wantDescriptors,
			maxRenderedSourceDescriptors,
		)
	}
	providedDescriptors := 5 +
		len(sources.ManagedDirectories) +
		len(sources.Projects) +
		len(sources.Binds) +
		len(sources.RuntimeAssets)
	if providedDescriptors > maxRenderedSourceDescriptors {
		return fmt.Errorf(
			"bubblewrap sources provide %d descriptors, limit is %d",
			providedDescriptors,
			maxRenderedSourceDescriptors,
		)
	}

	return nil
}

func validateOwnedDescriptorIdentities(plan Plan, sources Sources) error {
	descriptors := []namedDescriptor{
		{label: "rootfs", file: sources.RootFS},
		{label: "overlay upper", file: sources.OverlayUpper},
		{label: "overlay work", file: sources.OverlayWork},
		{label: "private home", file: sources.Home},
	}
	for _, entry := range plan.ManagedDirectories {
		descriptors = append(descriptors, namedDescriptor{
			label: "managed-directory " + entry.Key.String(),
			file:  sources.ManagedDirectories[entry.Key],
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
		identities = append(identities, descriptorIdentity{
			label: descriptor.label,
			info:  info,
		})
	}

	for index, identity := range identities {
		for earlier := range index {
			if os.SameFile(identity.info, identities[earlier].info) {
				return fmt.Errorf(
					"%s and %s source descriptors alias the same directory",
					identity.label,
					identities[earlier].label,
				)
			}
		}
	}
	upperDevice, err := descriptorDevice(identities[1])
	if err != nil {
		return fmt.Errorf("inspect overlay upper source filesystem: %w", err)
	}
	workDevice, err := descriptorDevice(identities[2])
	if err != nil {
		return fmt.Errorf("inspect overlay work source filesystem: %w", err)
	}
	if upperDevice != workDevice {
		return fmt.Errorf(
			"overlay upper and work source descriptors must be on the same filesystem",
		)
	}

	return nil
}

type namedDescriptor struct {
	label string
	file  *os.File
}

type descriptorIdentity struct {
	label string
	info  fs.FileInfo
}

func descriptorDevice(identity descriptorIdentity) (uint64, error) {
	stat, ok := identity.info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, fmt.Errorf(
			"%s descriptor has unavailable filesystem identity",
			identity.label,
		)
	}
	return uint64(stat.Dev), nil
}

func validateManagedCoverage(
	entries []mount.Entry,
	sources map[mount.Key]*os.File,
) error {
	expected := make(map[mount.Key]struct{}, len(entries))
	for _, entry := range entries {
		expected[entry.Key] = struct{}{}
	}

	var unexpected []string
	for key := range sources {
		if _, found := expected[key]; !found {
			unexpected = append(unexpected, key.String())
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)

	return fmt.Errorf(
		"unexpected managed-directory source %q",
		unexpected[0],
	)
}

func validateStringCoverage[T any](
	kind string,
	expected []string,
	sources map[string]T,
) error {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, key := range expected {
		expectedSet[key] = struct{}{}
	}

	var unexpected []string
	for key := range sources {
		if _, found := expectedSet[key]; !found {
			unexpected = append(unexpected, key)
		}
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)

	return fmt.Errorf("unexpected %s source %q", kind, unexpected[0])
}

func projectNames(projects []Project) []string {
	names := make([]string, len(projects))
	for index, project := range projects {
		names[index] = project.Name
	}
	return names
}

func bindTargets(binds []mount.Bind) []string {
	targets := make([]string, len(binds))
	for index, bind := range binds {
		targets[index] = bind.Target
	}
	return targets
}

func runtimeAssetTargets(assets []RuntimeAsset) []string {
	targets := make([]string, len(assets))
	for index, asset := range assets {
		targets[index] = asset.Target
	}
	return targets
}

func validateDirectoryDescriptor(label string, file *os.File) error {
	info, err := descriptorInfo(label, file)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"%s source must be a directory descriptor, got %s",
			label,
			descriptorKind(info.Mode()),
		)
	}
	return nil
}

func validateExecutableDescriptor(label string, file *os.File) error {
	info, err := descriptorInfo(label, file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf(
			"%s source must be a regular-file descriptor, got mode %v",
			label,
			info.Mode(),
		)
	}
	return nil
}

func validateBindDescriptor(
	label string,
	file *os.File,
	access mount.Access,
) error {
	info, err := descriptorInfo(label, file)
	if err != nil {
		return err
	}
	mode := info.Mode()
	if access == mount.AccessDev {
		if mode&os.ModeSocket == 0 {
			return fmt.Errorf(
				"%s device-access source must be a Unix socket descriptor, got %s",
				label,
				descriptorKind(mode),
			)
		}
		return nil
	}
	if mode.IsDir() || mode.IsRegular() || mode&os.ModeSocket != 0 {
		return nil
	}

	return fmt.Errorf(
		"%s source has unsupported descriptor kind %s",
		label,
		descriptorKind(mode),
	)
}

func descriptorInfo(label string, file *os.File) (fs.FileInfo, error) {
	if file == nil {
		return nil, fmt.Errorf("%s source descriptor is nil", label)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s source descriptor: %w", label, err)
	}
	return info, nil
}

func descriptorKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "regular file"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeDevice != 0:
		return "device"
	default:
		return mode.Type().String()
	}
}

func duplicateDescriptor(file *os.File, label string) (*os.File, error) {
	if file == nil {
		return nil, fmt.Errorf("%s source descriptor is nil", label)
	}

	raw, err := file.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("access %s source descriptor: %w", label, err)
	}

	duplicateFD := -1
	var duplicateErr error
	if err := raw.Control(func(source uintptr) {
		duplicateFD, duplicateErr = unix.FcntlInt(
			source,
			unix.F_DUPFD_CLOEXEC,
			0,
		)
	}); err != nil {
		return nil, fmt.Errorf("retain %s source descriptor: %w", label, err)
	}
	if duplicateErr != nil {
		return nil, fmt.Errorf(
			"retain %s source descriptor: %w",
			label,
			duplicateErr,
		)
	}
	if duplicateFD < 0 {
		return nil, fmt.Errorf(
			"retain %s source descriptor: duplicate was not created",
			label,
		)
	}

	duplicate := os.NewFile(uintptr(duplicateFD), label)
	if duplicate == nil {
		diagnostic.DiscardError(
			"source retention has no diagnostic logger",
			"close invalid source descriptor duplicate",
			unix.Close(duplicateFD),
			"label", label,
		)
		return nil, fmt.Errorf(
			"retain %s source descriptor: invalid duplicate",
			label,
		)
	}
	return duplicate, nil
}
