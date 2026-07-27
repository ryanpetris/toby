package bwrap

// Verifies deterministic FD-backed Bubblewrap argument rendering without
// opening any diagnostic host path.

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/mount"
)

func TestRenderGoldenOrderFDsAccessAndEnvironment(t *testing.T) {
	plan := rendererPlan()
	sources := rendererSources(t, plan)

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	wantArgs := []string{
		"--unshare-user",
		"--uid", "1000",
		"--gid", "1000",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--cap-drop", "ALL",
		"--overlay-src", "/proc/self/fd/3",
		"--overlay", "/proc/self/fd/4", "/proc/self/fd/5", "/",
		"--ro-bind-fd", "3", "/dev",
		"--ro-bind-fd", "4", "/dev",
		"--ro-bind-fd", "5", "/dev",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/run",
		"--dir", "/run/toby",
		"--chmod", "0700", "/run/toby",
		"--dir", "/toby",
		"--dir", "/toby/home",
		"--dir", "/toby/workspace",
		"--dir", "/toby/bin",
		"--bind-fd", "6", "/toby/home",
		"--bind-fd", "7", "/toby/home/.local/share/opencode",
		"--bind-fd", "8", "/toby/workspace/app",
		"--ro-bind-fd", "9", "/toby/workspace/zeta",
		"--ro-bind-fd", "10", "/etc/toby-config",
		"--bind-fd", "11", "/opt/toby-cache",
		"--bind-fd", "12", "/var/run/docker.sock",
		"--ro-bind-fd", "13", "/run/toby/a",
		"--bind-fd", "14", "/run/toby/docker.sock",
		"--bind-fd", "15", "/run/toby/z",
		"--ro-bind-fd", "16", "/toby/bin/tobys",
		"--clearenv",
		"--setenv", "HOME", "/toby/home",
		"--setenv", "TOBY_SANDBOX", "1",
		"--setenv", "A_FIRST", "first",
		"--setenv", "Z_LAST", "last",
		"--chdir", "/toby/workspace/app",
		"--",
		"/bin/echo", "literal value",
	}
	if !reflect.DeepEqual(invocation.Args, wantArgs) {
		t.Fatalf(
			"rendered arguments differ:\n got: %q\nwant: %q",
			invocation.Args,
			wantArgs,
		)
	}
	if invocation.Mode != plan.Command.Mode {
		t.Fatalf(
			"invocation mode = %q, want %q",
			invocation.Mode,
			plan.Command.Mode,
		)
	}

	canonical := plan.Canonical()
	wantSources := []*os.File{
		sources.RootFS,
		sources.OverlayUpper,
		sources.OverlayWork,
		sources.Home,
	}
	for _, entry := range canonical.ManagedDirectories {
		wantSources = append(wantSources, sources.ManagedDirectories[entry.Key])
	}
	for _, project := range canonical.Projects {
		wantSources = append(wantSources, sources.Projects[project.Name])
	}
	for _, bind := range canonical.Binds {
		wantSources = append(wantSources, sources.Binds[bind.Target])
	}
	for _, asset := range canonical.RuntimeAssets {
		wantSources = append(wantSources, sources.RuntimeAssets[asset.Target])
	}
	wantSources = append(wantSources, sources.SandboxBinary)

	if len(invocation.ExtraFiles) != len(wantSources) {
		t.Fatalf(
			"extra files = %d, want %d",
			len(invocation.ExtraFiles),
			len(wantSources),
		)
	}
	for index, want := range wantSources {
		assertSameDescriptorObject(
			t,
			invocation.ExtraFiles[index],
			want,
			index+childExtraFileBaseFD,
		)
	}
}

func TestRenderUsesOpenedCapabilitiesAfterDiagnosticPathsChange(t *testing.T) {
	plan := validPlan()
	plan.RootFS.Path = "/diagnostic/rootfs"
	plan.Overlay.RunStorageDir = "/diagnostic/runs"
	plan.Overlay.Upper = "/diagnostic/runs/" + plan.RunID + "/upper"
	plan.Overlay.Work = "/diagnostic/runs/" + plan.RunID + "/work"
	plan.Home.HostPath = "/diagnostic/volumes/home/_data"
	plan.ManagedDirectories[0].HostPath = "/diagnostic/volumes/tool/_data"
	plan.GeneratedFiles[0].HostPath =
		plan.Home.HostPath + "/.config/opencode/opencode.json"
	plan.Projects[0].HostPath = "/diagnostic/projects/app"
	plan.SandboxBinary.HostPath = "/diagnostic/bin/toby"

	sources := rendererSources(t, plan)
	rootInfo, err := sources.RootFS.Stat()
	if err != nil {
		t.Fatal(err)
	}

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	retainedRoot := invocation.ExtraFiles[0]
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := retainedRoot.Stat(); err == nil {
		t.Fatal("invocation close left a retained source descriptor open")
	}

	callerInfo, err := sources.RootFS.Stat()
	if err != nil {
		t.Fatalf("invocation close closed a caller-owned descriptor: %v", err)
	}
	if !os.SameFile(rootInfo, callerInfo) {
		t.Fatal("caller-owned descriptor changed identity")
	}
}

func TestRenderKeepsCommandArgumentsLiteral(t *testing.T) {
	plan := validPlan()
	plan.Command.Argv = []string{
		"/bin/tool;still-one-argument",
		"$(touch /tmp/toby-must-not-exist)",
		"space separated",
		"`uname`",
		"quote'\"",
	}
	sources := rendererSources(t, plan)

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	separator := -1
	for index, argument := range invocation.Args {
		if argument == "--" {
			separator = index
		}
	}
	if separator < 0 {
		t.Fatal("rendered arguments have no command separator")
	}
	if got := invocation.Args[separator+1:]; !reflect.DeepEqual(got, plan.Command.Argv) {
		t.Fatalf("command arguments = %q, want literal %q", got, plan.Command.Argv)
	}
}

func TestRenderRootLifecycleUsesExplicitMinimalCapabilities(t *testing.T) {
	plan := validPlan()
	plan.Command.Root = true
	plan.Command.Capabilities = CapabilityRootLifecycle
	sources := rendererSources(t, plan)

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := invocation.Close(); err != nil {
			t.Error(err)
		}
	})

	wantPrefix := []string{
		"--unshare-user",
		"--uid", "0",
		"--gid", "0",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--cap-drop", "ALL",
		"--cap-add", "CAP_CHOWN",
		"--cap-add", "CAP_DAC_OVERRIDE",
		"--cap-add", "CAP_FOWNER",
		"--cap-add", "CAP_FSETID",
		"--cap-add", "CAP_SETGID",
		"--cap-add", "CAP_SETUID",
	}
	gotPrefix := invocation.Args
	if len(gotPrefix) > len(wantPrefix) {
		gotPrefix = gotPrefix[:len(wantPrefix)]
	}
	if len(invocation.Args) < len(wantPrefix) ||
		!reflect.DeepEqual(gotPrefix, wantPrefix) {
		t.Fatalf(
			"root lifecycle prefix = %q, want %q",
			gotPrefix,
			wantPrefix,
		)
	}
	for _, forbidden := range []string{
		"CAP_SYS_ADMIN",
		"CAP_SYS_PTRACE",
		"CAP_MKNOD",
		"CAP_NET_ADMIN",
		"CAP_NET_RAW",
	} {
		if slicesContain(invocation.Args, forbidden) {
			t.Fatalf("root lifecycle received forbidden capability %s", forbidden)
		}
	}
}

func TestRenderRejectsIncompleteOrMismatchedSources(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Plan, *Sources)
		match  string
	}{
		{
			name: "missing rootfs",
			mutate: func(_ *Plan, sources *Sources) {
				sources.RootFS = nil
			},
			match: "rootfs source descriptor is nil",
		},
		{
			name: "missing managed directory",
			mutate: func(plan *Plan, sources *Sources) {
				delete(sources.ManagedDirectories, plan.ManagedDirectories[0].Key)
			},
			match: "missing managed-directory source",
		},
		{
			name: "unexpected project",
			mutate: func(_ *Plan, sources *Sources) {
				sources.Projects["extra"] = sources.Home
			},
			match: "unexpected project source",
		},
		{
			name: "rootfs is a file",
			mutate: func(_ *Plan, sources *Sources) {
				sources.RootFS = sources.SandboxBinary
			},
			match: "rootfs source must be a directory descriptor",
		},
		{
			name: "upper and work alias",
			mutate: func(_ *Plan, sources *Sources) {
				sources.OverlayWork = sources.OverlayUpper
			},
			match: "alias the same directory",
		},
		{
			name: "rootfs and home alias",
			mutate: func(_ *Plan, sources *Sources) {
				sources.Home = sources.RootFS
			},
			match: "alias the same directory",
		},
		{
			name: "home and managed alias",
			mutate: func(plan *Plan, sources *Sources) {
				sources.ManagedDirectories[plan.ManagedDirectories[0].Key] =
					sources.Home
			},
			match: "alias the same directory",
		},
		{
			name: "device access is not a socket",
			mutate: func(plan *Plan, sources *Sources) {
				plan.Binds = []mount.Bind{{
					HostPath: "/host/not-a-device-tree",
					Target:   "/var/run/docker.sock",
					Access:   mount.AccessDev,
				}}
				sources.Binds = map[string]*os.File{
					"/var/run/docker.sock": sources.OverlayUpper,
				}
			},
			match: "device-access source must be a Unix socket descriptor",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validPlan()
			sources := rendererSources(t, plan)
			test.mutate(&plan, &sources)

			invocation, err := Render(plan, sources)
			if invocation != nil {
				t.Cleanup(func() {
					if err := invocation.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Render error = %v, want text %q", err, test.match)
			}
		})
	}
}

func TestRenderRejectsOversizedSourcePlanBeforeCanonicalization(t *testing.T) {
	plan := validPlan()
	plan.Binds = make(
		[]mount.Bind,
		maxRenderedSourceDescriptors,
	)

	invocation, err := Render(plan, Sources{})
	if invocation != nil {
		t.Cleanup(func() {
			if err := invocation.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err == nil || !strings.Contains(err.Error(), "source descriptors, limit") {
		t.Fatalf("Render error = %v, want descriptor limit", err)
	}
}

func TestRenderAcceptsSandboxBinaryPermissionBits(t *testing.T) {
	plan := validPlan()
	sources := rendererSources(t, plan)
	if err := os.Chmod(sources.SandboxBinary.Name(), os.ModeSetuid|0o700); err != nil {
		t.Fatal(err)
	}

	invocation, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
}

func rendererPlan() Plan {
	plan := validPlan()
	plan.Projects = append(plan.Projects,
		Project{
			Name:     "zeta",
			HostPath: "/projects/zeta",
			Target:   "/toby/workspace/zeta",
			ReadOnly: true,
		},
	)
	plan.Binds = []mount.Bind{
		{
			HostPath: "/host/docker.sock",
			Target:   "/var/run/docker.sock",
			Access:   mount.AccessDev,
		},
		{
			HostPath: "/host/cache",
			Target:   "/opt/toby-cache",
			Access:   mount.AccessRegular,
		},
		{
			HostPath: "/host/config",
			Target:   "/etc/toby-config",
			Access:   mount.AccessReadOnly,
		},
	}
	plan.RuntimeAssets = []RuntimeAsset{
		{
			HostPath: "/host/runtime/z",
			Target:   "/run/toby/z",
			Access:   mount.AccessRegular,
		},
		{
			HostPath: "/host/runtime/docker.sock",
			Target:   "/run/toby/docker.sock",
			Access:   mount.AccessDev,
		},
		{
			HostPath: "/host/runtime/a",
			Target:   "/run/toby/a",
			Access:   mount.AccessReadOnly,
		},
	}
	plan.Environment = []EnvironmentVariable{
		{Name: "Z_LAST", Value: "last"},
		{Name: "A_FIRST", Value: "first"},
	}
	plan.Command.Argv = []string{"/bin/echo", "literal value"}

	return plan
}

func rendererSources(t *testing.T, plan Plan) Sources {
	t.Helper()

	base := t.TempDir()
	persistentDataPath := filepath.Join(base, "data")
	imageStorePath := filepath.Join(persistentDataPath, "images")
	rootfsPath := filepath.Join(imageStorePath, "rootfs", "selected")
	homePath := filepath.Join(
		persistentDataPath,
		"volumes",
		"home-id",
		"_data",
	)
	runStoragePath := filepath.Join(base, "cache", "runs")
	runRoot := filepath.Join(runStoragePath, plan.RunID)
	runtimePath := filepath.Join(base, "runtime", "toby")
	for _, directory := range []string{
		rootfsPath,
		homePath,
		filepath.Join(runRoot, "upper"),
		filepath.Join(runRoot, "work"),
		runtimePath,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	sources := Sources{
		ProtectedRoots: ProtectedRoots{
			ImageStore:     openDirectorySourceAt(t, imageStorePath),
			PersistentData: openDirectorySourceAt(t, persistentDataPath),
			RunStorage:     openDirectorySourceAt(t, runStoragePath),
			Runtime:        openDirectorySourceAt(t, runtimePath),
		},
		RootFS:             openDirectorySourceAt(t, rootfsPath),
		OverlayUpper:       openDirectorySourceAt(t, filepath.Join(runRoot, "upper")),
		OverlayWork:        openDirectorySourceAt(t, filepath.Join(runRoot, "work")),
		Home:               openDirectorySourceAt(t, homePath),
		ManagedDirectories: make(map[mount.Key]*os.File),
		Projects:           make(map[string]*os.File),
		Binds:              make(map[string]*os.File),
		BindParents:        make(map[string]*os.File),
		BindNames:          make(map[string]string),
		RuntimeAssets:      make(map[string]*os.File),
		SandboxBinary:      openExecutableSource(t),
	}
	for index, entry := range plan.ManagedDirectories {
		directory := filepath.Join(
			persistentDataPath,
			"volumes",
			strconv.Itoa(index),
			"_data",
		)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		sources.ManagedDirectories[entry.Key] = openDirectorySourceAt(
			t,
			directory,
		)
	}
	for _, project := range plan.Projects {
		directory := filepath.Join(base, "projects", project.Name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		sources.Projects[project.Name] = openDirectorySourceAt(t, directory)
	}
	for index, bind := range plan.Binds {
		source, parent := openBindSource(t, base, index, bind)
		sources.Binds[bind.Target] = source
		sources.BindParents[bind.Target] = parent
		sources.BindNames[bind.Target] = filepath.Base(bind.HostPath)
	}
	for _, asset := range plan.RuntimeAssets {
		if asset.Access == mount.AccessDev {
			sources.RuntimeAssets[asset.Target] = openSocketSource(t)
			continue
		}
		sources.RuntimeAssets[asset.Target] = openDirectorySource(t)
	}

	return sources
}

func openDirectorySourceAt(t *testing.T, path string) *os.File {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Error(err)
		}
	})

	return file
}

func openDirectorySource(t *testing.T) *os.File {
	t.Helper()

	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Error(err)
		}
	})

	return file
}

func openExecutableSource(t *testing.T) *os.File {
	t.Helper()

	name := filepath.Join(t.TempDir(), "toby")
	if err := os.WriteFile(name, []byte("test binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Error(err)
		}
	})

	return file
}

func openSocketSource(t *testing.T) *os.File {
	t.Helper()

	name := filepath.Join(t.TempDir(), "source.sock")
	return openSocketSourceAt(t, name)
}

func openSocketSourceAt(t *testing.T, name string) *os.File {
	t.Helper()

	listener, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	})

	fd, err := unix.Open(
		name,
		unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "socket source")
	if file == nil {
		unix.Close(fd)
		t.Fatal("create socket source descriptor")
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrInvalid) {
			t.Error(err)
		}
	})

	return file
}

func openBindSource(
	t *testing.T,
	base string,
	index int,
	bind mount.Bind,
) (*os.File, *os.File) {
	t.Helper()

	parentPath := filepath.Join(base, "binds", strconv.Itoa(index))
	if err := os.MkdirAll(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(parentPath, filepath.Base(bind.HostPath))

	var source *os.File
	if bind.Access == mount.AccessDev {
		source = openSocketSourceAt(t, sourcePath)
	} else {
		if err := os.Mkdir(sourcePath, 0o700); err != nil {
			t.Fatal(err)
		}
		source = openDirectorySourceAt(t, sourcePath)
	}

	return source, openDirectorySourceAt(t, parentPath)
}

func assertSameDescriptorObject(
	t *testing.T,
	got *os.File,
	want *os.File,
	childFD int,
) {
	t.Helper()

	gotInfo, err := got.Stat()
	if err != nil {
		t.Fatalf("stat retained child FD %d: %v", childFD, err)
	}
	wantInfo, err := want.Stat()
	if err != nil {
		t.Fatalf("stat source for child FD %d: %v", childFD, err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("child FD %d does not retain the expected source", childFD)
	}
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
