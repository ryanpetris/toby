package docker

import (
	"errors"
	"os"
	"sort"

	"petris.dev/toby/container/mount"

	dmount "github.com/moby/moby/api/types/mount"
)

type targetedMount struct {
	target string
	mount  dmount.Mount
}

// finalMounts builds the mount set for the Prime and Run containers: project
// binds, explicit binds (skipping missing optional ones), and persistent volumes,
// ordered stably by target.
func (s *instance) finalMounts(binds []mount.Bind, volumes []mount.Entry) []dmount.Mount {
	items := make([]targetedMount, 0, len(binds)+len(volumes)+1)
	for _, project := range s.ProjectMounts() {
		items = append(items, targetedMount{target: project.SandboxPath, mount: bindMount(project.HostPath, project.SandboxPath, false)})
	}
	for _, bind := range binds {
		if bind.Optional {
			if _, err := os.Stat(bind.HostPath); err != nil && errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		items = append(items, targetedMount{target: bind.Target, mount: bindMount(bind.HostPath, bind.Target, bind.Access == mount.AccessReadOnly)})
	}
	for _, m := range volumes {
		items = append(items, targetedMount{target: m.Target, mount: volumeMount(m.Volume, m.Target)})
	}
	return orderedMounts(items)
}

// setupMounts mounts volumes at their setup paths so the root Setup container can
// initialize them (the default is a chown to the host user).
func (s *instance) setupMounts(volumes []mount.Entry) []dmount.Mount {
	items := make([]targetedMount, 0, len(volumes))
	for _, m := range volumes {
		if m.SetupPath != "" {
			items = append(items, targetedMount{target: m.SetupPath, mount: volumeMount(m.Volume, m.SetupPath)})
		}
	}
	return orderedMounts(items)
}

func orderedMounts(items []targetedMount) []dmount.Mount {
	sort.SliceStable(items, func(i, j int) bool { return items[i].target < items[j].target })
	mounts := make([]dmount.Mount, 0, len(items))
	for _, item := range items {
		mounts = append(mounts, item.mount)
	}
	return mounts
}

func bindMount(source, target string, readonly bool) dmount.Mount {
	return dmount.Mount{Type: dmount.TypeBind, Source: source, Target: target, ReadOnly: readonly}
}

func volumeMount(name, target string) dmount.Mount {
	return dmount.Mount{Type: dmount.TypeVolume, Source: name, Target: target}
}
