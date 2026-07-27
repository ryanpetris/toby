//go:build linux

package bwrap

// Validates project descriptors against the authoritative directory ancestry
// of protected sandbox storage, independently of diagnostic host paths.

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

type directoryLineage struct {
	label string
	infos []fs.FileInfo
}

func validateProjectDescriptorIsolation(plan Plan, sources Sources) error {
	runRoot, err := openDescriptorParent(
		sources.OverlayUpper,
		"overlay upper",
	)
	if err != nil {
		return err
	}
	defer func() {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close overlay run-root descriptor",
			runRoot.Close(),
		)
	}()

	workRoot, err := openDescriptorParent(
		sources.OverlayWork,
		"overlay work",
	)
	if err != nil {
		return err
	}
	defer func() {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close overlay work-root descriptor",
			workRoot.Close(),
		)
	}()

	runRootInfo, err := descriptorInfo("overlay run root", runRoot)
	if err != nil {
		return err
	}
	workRootInfo, err := descriptorInfo("overlay work parent", workRoot)
	if err != nil {
		return err
	}
	if !os.SameFile(runRootInfo, workRootInfo) {
		return fmt.Errorf(
			"overlay upper and work source descriptors must share one run root",
		)
	}

	runStorageParent, err := openDescriptorParent(runRoot, "overlay run root")
	if err != nil {
		return err
	}
	defer func() {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close Bubblewrap run-storage parent descriptor",
			runStorageParent.Close(),
		)
	}()
	runStorageParentInfo, err := descriptorInfo(
		"overlay run-root parent",
		runStorageParent,
	)
	if err != nil {
		return err
	}
	runStorageInfo, err := descriptorInfo(
		"Bubblewrap run-storage root",
		sources.ProtectedRoots.RunStorage,
	)
	if err != nil {
		return err
	}
	if !os.SameFile(runStorageParentInfo, runStorageInfo) {
		return fmt.Errorf(
			"overlay run root is not a direct child of the authoritative Bubblewrap run-storage root",
		)
	}

	protected := []namedDescriptor{
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
	}

	protectedLineages := make([]directoryLineage, 0, len(protected))
	for _, descriptor := range protected {
		lineage, err := inspectDirectoryLineage(descriptor)
		if err != nil {
			return err
		}
		protectedLineages = append(protectedLineages, lineage)
	}

	if !directoryStrictlyContains(
		protectedLineages[1],
		protectedLineages[0],
	) {
		return fmt.Errorf(
			"OCI image-store root is not strictly beneath the Toby persistent-data root",
		)
	}

	rootfsLineage, err := inspectDirectoryLineage(namedDescriptor{
		label: "rootfs",
		file:  sources.RootFS,
	})
	if err != nil {
		return err
	}
	if !directoryStrictlyContains(protectedLineages[0], rootfsLineage) {
		return fmt.Errorf(
			"rootfs source descriptor is not strictly beneath the OCI image-store root",
		)
	}

	homeLineage, err := inspectDirectoryLineage(namedDescriptor{
		label: "private home",
		file:  sources.Home,
	})
	if err != nil {
		return err
	}
	if !directoryStrictlyContains(protectedLineages[1], homeLineage) {
		return fmt.Errorf(
			"private-home source descriptor is not strictly beneath the Toby persistent-data root",
		)
	}
	for _, entry := range plan.ManagedDirectories {
		managedLineage, err := inspectDirectoryLineage(namedDescriptor{
			label: "managed-directory " + entry.Key.String(),
			file:  sources.ManagedDirectories[entry.Key],
		})
		if err != nil {
			return err
		}
		if !directoryStrictlyContains(
			protectedLineages[1],
			managedLineage,
		) {
			return fmt.Errorf(
				"%s source descriptor is not strictly beneath the Toby persistent-data root",
				managedLineage.label,
			)
		}
	}

	for _, project := range plan.Projects {
		projectLineage, err := inspectDirectoryLineage(namedDescriptor{
			label: "project " + project.Name,
			file:  sources.Projects[project.Name],
		})
		if err != nil {
			return err
		}

		for _, protectedLineage := range protectedLineages {
			if directoryLineagesOverlap(projectLineage, protectedLineage) {
				return fmt.Errorf(
					"%s source descriptor overlaps protected path rooted at %s",
					projectLineage.label,
					protectedLineage.label,
				)
			}
		}
	}

	return nil
}

func directoryStrictlyContains(
	parent directoryLineage,
	child directoryLineage,
) bool {
	if len(parent.infos) == 0 || len(child.infos) == 0 ||
		os.SameFile(parent.infos[0], child.infos[0]) {
		return false
	}

	for _, info := range child.infos[1:] {
		if os.SameFile(info, parent.infos[0]) {
			return true
		}
	}

	return false
}

func inspectDirectoryLineage(
	descriptor namedDescriptor,
) (directoryLineage, error) {
	current, err := duplicateDescriptor(descriptor.file, descriptor.label)
	if err != nil {
		return directoryLineage{}, err
	}
	defer func() {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close directory-lineage descriptor",
			current.Close(),
			"label", descriptor.label,
		)
	}()

	lineage := directoryLineage{label: descriptor.label}
	for {
		info, err := descriptorInfo(descriptor.label, current)
		if err != nil {
			return directoryLineage{}, err
		}
		lineage.infos = append(lineage.infos, info)

		parent, err := openDescriptorParent(current, descriptor.label)
		if err != nil {
			return directoryLineage{}, err
		}
		parentInfo, err := descriptorInfo(descriptor.label+" parent", parent)
		if err != nil {
			diagnostic.DiscardError(
				"source validation has no diagnostic logger",
				"close uninspected directory-lineage parent",
				parent.Close(),
				"label", descriptor.label,
			)
			return directoryLineage{}, err
		}
		if os.SameFile(info, parentInfo) {
			diagnostic.DiscardError(
				"source validation has no diagnostic logger",
				"close directory-lineage filesystem root",
				parent.Close(),
				"label", descriptor.label,
			)
			return lineage, nil
		}

		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close traversed directory-lineage descriptor",
			current.Close(),
			"label", descriptor.label,
		)
		current = parent
	}
}

func openDescriptorParent(file *os.File, label string) (*os.File, error) {
	if file == nil {
		return nil, fmt.Errorf("%s source descriptor is nil", label)
	}

	parentFD, err := unix.Openat(
		int(file.Fd()),
		"..",
		unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s source parent: %w", label, err)
	}

	parent := os.NewFile(uintptr(parentFD), label+" parent")
	if parent == nil {
		diagnostic.DiscardError(
			"source validation has no diagnostic logger",
			"close invalid source-parent descriptor",
			unix.Close(parentFD),
			"label", label,
		)
		return nil, fmt.Errorf(
			"open %s source parent: invalid descriptor",
			label,
		)
	}

	return parent, nil
}

func directoryLineagesOverlap(
	first directoryLineage,
	second directoryLineage,
) bool {
	if len(first.infos) == 0 || len(second.infos) == 0 {
		return false
	}

	for _, info := range first.infos {
		if os.SameFile(info, second.infos[0]) {
			return true
		}
	}
	for _, info := range second.infos {
		if os.SameFile(info, first.infos[0]) {
			return true
		}
	}

	return false
}
