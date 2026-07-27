//go:build linux

package bwrap

// Validates external-bind parent/child identity and descriptor-authoritative
// isolation from protected and Toby-owned storage.

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/mount"
)

func validateExternalBindParent(
	bind mount.Bind,
	source *os.File,
	parent *os.File,
	base string,
) error {
	if base == "" ||
		base == "." ||
		base == string(filepath.Separator) ||
		filepath.Base(base) != base {
		return fmt.Errorf(
			"external bind %s has invalid resolved basename %q",
			bind.Target,
			base,
		)
	}

	childFD, err := unix.Openat2(
		int(parent.Fd()),
		base,
		&unix.OpenHow{
			Flags: unix.O_PATH | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_MAGICLINKS |
				unix.RESOLVE_NO_SYMLINKS,
		},
	)
	if err != nil {
		return fmt.Errorf(
			"open external bind %s as exact parent child %q: %w",
			bind.Target,
			base,
			err,
		)
	}
	child := os.NewFile(uintptr(childFD), "validated external bind "+bind.Target)
	if child == nil {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close invalid external-bind child descriptor",
			unix.Close(childFD),
			"target", bind.Target,
		)
		return fmt.Errorf(
			"open external bind %s as exact parent child: invalid descriptor",
			bind.Target,
		)
	}
	defer func() {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close validated external-bind child descriptor",
			child.Close(),
			"target", bind.Target,
		)
	}()

	sourceInfo, err := descriptorInfo("external bind "+bind.Target, source)
	if err != nil {
		return err
	}
	childInfo, err := descriptorInfo(
		"external bind "+bind.Target+" parent child",
		child,
	)
	if err != nil {
		return err
	}
	if !os.SameFile(sourceInfo, childInfo) {
		return fmt.Errorf(
			"external bind %s source is not the exact basename child of its retained parent",
			bind.Target,
		)
	}

	return nil
}

func validateExternalBindLinkShape(label string, source *os.File) error {
	info, err := descriptorInfo(label, source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fmt.Errorf("%s source has unavailable link identity", label)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf(
			"%s non-directory source has unsafe link count %d, want exactly 1",
			label,
			stat.Nlink,
		)
	}

	return nil
}

func validateBindDescriptorIsolation(
	plan Plan,
	sources Sources,
) error {
	protected, err := inspectDescriptorLineages([]namedDescriptor{
		{
			label: "OCI image-store root",
			file:  sources.ProtectedRoots.ImageStore,
		},
		{
			label: "Toby persistent-data root",
			file:  sources.ProtectedRoots.PersistentData,
		},
		{
			label: "Bubblewrap run-storage root",
			file:  sources.ProtectedRoots.RunStorage,
		},
		{
			label: "Toby runtime root",
			file:  sources.ProtectedRoots.Runtime,
		},
	})
	if err != nil {
		return err
	}

	ownedDescriptors := []namedDescriptor{
		{label: "rootfs", file: sources.RootFS},
		{label: "overlay upper", file: sources.OverlayUpper},
		{label: "overlay work", file: sources.OverlayWork},
		{label: "private home", file: sources.Home},
	}
	for _, entry := range plan.ManagedDirectories {
		ownedDescriptors = append(ownedDescriptors, namedDescriptor{
			label: "managed-directory " + entry.Key.String(),
			file:  sources.ManagedDirectories[entry.Key],
		})
	}
	owned, err := inspectDescriptorLineages(ownedDescriptors)
	if err != nil {
		return err
	}

	for _, bind := range plan.Binds {
		source := sources.Binds[bind.Target]
		info, err := descriptorInfo("external bind "+bind.Target, source)
		if err != nil {
			return err
		}

		lineageSource := source
		lineageLabel := "external bind " + bind.Target
		if !info.IsDir() {
			lineageSource = sources.BindParents[bind.Target]
			lineageLabel += " parent"
		}
		lineage, err := inspectDirectoryLineage(namedDescriptor{
			label: lineageLabel,
			file:  lineageSource,
		})
		if err != nil {
			return err
		}

		for _, protectedLineage := range protected {
			if directoryLineagesOverlap(lineage, protectedLineage) {
				return fmt.Errorf(
					"%s source lineage overlaps protected path rooted at %s",
					lineage.label,
					protectedLineage.label,
				)
			}
		}
		for _, ownedLineage := range owned {
			if directoryLineagesOverlap(lineage, ownedLineage) {
				return fmt.Errorf(
					"%s source lineage overlaps Toby-owned backing %s",
					lineage.label,
					ownedLineage.label,
				)
			}
		}
	}

	return nil
}

func inspectDescriptorLineages(
	descriptors []namedDescriptor,
) ([]directoryLineage, error) {
	lineages := make([]directoryLineage, 0, len(descriptors))
	for _, descriptor := range descriptors {
		lineage, err := inspectDirectoryLineage(descriptor)
		if err != nil {
			return nil, err
		}
		lineages = append(lineages, lineage)
	}

	return lineages, nil
}
