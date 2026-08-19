//go:build linux

package run

// Exercises complete native run assembly, generated-file publication,
// transient assets, shared execution, and lease-ordered teardown.

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/runtimeassets"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/storage/safefs"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
)

type nativeTestImage struct {
	*nativeTestDirectory
	spec oci.Spec
}

var _ PreparedImage = (*nativeTestImage)(nil)

func (i *nativeTestImage) RootfsPath() string {
	return i.path
}

func (i *nativeTestImage) RootfsFile() (*os.File, error) {
	return os.Open(i.path)
}

func (i *nativeTestImage) Spec() oci.Spec {
	return i.spec
}

type nativeTestHome struct {
	*nativeTestDirectory
	identity storage.HomeIdentity
}

var _ PrivateHome = (*nativeTestHome)(nil)

func (h *nativeTestHome) Identity() storage.HomeIdentity {
	return h.identity
}

func (h *nativeTestHome) HostPath() string {
	return h.path
}

func (h *nativeTestHome) File() (*os.File, error) {
	return os.Open(h.path)
}

type nativeTestManaged struct {
	*nativeTestDirectory
	entry mount.Entry
}

var _ ManagedDirectory = (*nativeTestManaged)(nil)

func (m *nativeTestManaged) Entry() mount.Entry {
	return m.entry
}

func (m *nativeTestManaged) File() (*os.File, error) {
	return os.Open(m.path)
}

type nativeTestDirectory struct {
	path   string
	closed bool
}

var _ io.Closer = (*nativeTestDirectory)(nil)

func (d *nativeTestDirectory) Close() error {
	d.closed = true
	return nil
}

type nativeRecordingExecutor struct {
	invocations int
}

var _ bwrap.ProcessExecutor = (*nativeRecordingExecutor)(nil)

func (e *nativeRecordingExecutor) Execute(
	_ context.Context,
	invocation *bwrap.Invocation,
	_ bwrap.ProcessIO,
) (int, error) {
	if invocation == nil || len(invocation.Args) == 0 {
		return 1, errors.New("empty invocation")
	}
	for _, file := range invocation.ExtraFiles {
		if _, err := file.Stat(); err != nil {
			return 1, err
		}
	}
	e.invocations++
	return 0, nil
}

type nativeLifecycleTool struct {
	tools.Base
	sandbox sandboxapi.Service
}

var _ tools.Tool = (*nativeLifecycleTool)(nil)
var _ lifecycle.LaunchPreparer = (*nativeLifecycleTool)(nil)

func (t *nativeLifecycleTool) PrepareHost(
	context.Context,
	*tools.Options,
) error {
	err := t.sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "agent",
			Purpose: "state",
		},
		Target: "~/.agent",
	})
	return err
}

func (t *nativeLifecycleTool) ConfigureSandbox(context.Context) error {
	return nil
}

func (t *nativeLifecycleTool) PrepareLaunch(
	ctx context.Context,
	args []string,
) error {
	value := "default"
	if len(args) != 0 {
		value = args[0]
	}
	return t.sandbox.SetEnvironment(ctx, "AGENT_MODEL", value)
}

func (t *nativeLifecycleTool) InitSandbox(ctx context.Context) error {
	_, err := t.sandbox.Exec(
		ctx,
		[]string{"/bin/initialize-agent"},
		sandboxapi.ExecOptions{},
	)
	return err
}

func (t *nativeLifecycleTool) Install(
	ctx context.Context,
	_ bool,
) error {
	_, err := t.sandbox.Exec(
		ctx,
		[]string{"/bin/install-agent"},
		sandboxapi.ExecOptions{},
	)
	return err
}

func (t *nativeLifecycleTool) Launch(
	ctx context.Context,
	args []string,
) error {
	_, err := t.sandbox.Exec(
		ctx,
		append([]string{"/bin/agent"}, args...),
		sandboxapi.ExecOptions{Foreground: true},
	)
	return err
}

func TestNativeRunPublishesFilesAndUsesOneBubblewrapRun(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(
		workingDirectory,
		".toby-native-run-test-",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	persistentDataPath := filepath.Join(base, "data")
	imageStorePath := filepath.Join(persistentDataPath, "images")
	rootfsPath := filepath.Join(imageStorePath, "rootfs", "selected")
	homePath := filepath.Join(
		persistentDataPath,
		"volumes",
		"home-id",
		"_data",
	)
	managedPath := filepath.Join(
		persistentDataPath,
		"volumes",
		"tool-id",
		"_data",
	)
	projectPath := filepath.Join(base, "project")
	runRuntimePath := filepath.Join(base, "run-runtime")
	hostRuntimePath := filepath.Join(base, "host-runtime")
	for _, directory := range []string{
		rootfsPath,
		homePath,
		managedPath,
		projectPath,
		runRuntimePath,
		hostRuntimePath,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	image := &nativeTestImage{
		nativeTestDirectory: &nativeTestDirectory{
			path: rootfsPath,
		},
		spec: oci.Spec{
			Manifest: ocispec.Descriptor{
				Digest: digest.Digest(
					"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				),
			},
			Runtime: oci.RuntimeConfig{Environment: []string{
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"LANG=C.UTF-8",
				"HOME=/root",
			}},
		},
	}
	home := &nativeTestHome{
		nativeTestDirectory: &nativeTestDirectory{
			path: homePath,
		},
		identity: storage.HomeIdentity{
			DisplayName: "same-home",
			ID:          "same-home-a1b2c3",
			Profile:     "default",
		},
	}
	managedEntry := mount.Entry{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "agent",
			Purpose: "state",
		},
		Profile:  "default",
		HostPath: managedPath,
		Target:   layout.Home + "/.agent",
		Access:   mount.AccessRegular,
	}
	managed := &nativeTestManaged{
		nativeTestDirectory: &nativeTestDirectory{
			path: managedEntry.HostPath,
		},
		entry: managedEntry,
	}

	project := bwrap.Project{
		Name:     "app",
		HostPath: projectPath,
		Target:   layout.Workspace + "/app",
	}
	toolSandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{
		Projects:         []bwrap.Project{project},
		ImageEnvironment: image.spec.Runtime.Environment,
	})
	if err != nil {
		t.Fatal(err)
	}

	toolRegistry, err := tools.NewRegistry([]tools.Tool{
		&nativeLifecycleTool{
			Base:    tools.Base{Metadata: tools.Metadata{Name: "agent"}},
			sandbox: toolSandbox,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := toolRegistry.Build([]string{"agent"}, "agent")
	if err != nil {
		t.Fatal(err)
	}
	nativeLifecycle := lifecycle.NewRunner(
		status.NewService(nil),
	)
	lifecycleContext := lifecycle.Context{Options: &tools.Options{}}
	primaryArgs := []string{"test-model"}
	if err := nativeLifecycle.PrepareHost(
		t.Context(),
		toolset,
		lifecycleContext,
	); err != nil {
		t.Fatal(err)
	}
	// A production launch resolves its agent session and populates the
	// sandbox-safe session holder at this explicit lifecycle boundary.
	if err := nativeLifecycle.Configure(
		t.Context(),
		toolset,
		lifecycleContext,
		primaryArgs,
	); err != nil {
		t.Fatal(err)
	}

	runtimeRoot, err := openNativeRunTestRoot(runRuntimePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, found := toolSandbox.Environment("AGENT_MODEL"); !found ||
		got != "test-model" {
		t.Fatalf("prepared launch environment = %q, %v", got, found)
	}
	if got, found := toolSandbox.Environment("PATH"); !found ||
		got != "/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("image PATH = %q, %v", got, found)
	}
	defer runtimeRoot.Close()
	assetRegistry, err := runtimeassets.NewRegistry([]runtimeassets.Asset{{
		Target: layout.Runtime + "/agent/wrapper",
		Data:   []byte("#!/bin/sh\n"),
		Mode:   0o500,
	}})
	if err != nil {
		t.Fatal(err)
	}
	gatewayPath := filepath.Join(base, "gateway.sock")
	gatewayListener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: gatewayPath, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayListener.Close()
	if err := os.Chmod(gatewayPath, 0o600); err != nil {
		t.Fatal(err)
	}
	var gatewayStatus unix.Stat_t
	if err := unix.Lstat(gatewayPath, &gatewayStatus); err != nil {
		t.Fatal(err)
	}
	gateway, err := sandboxgateway.OpenCapability(sandboxgateway.DescriptorConfig{
		HostSocket:       gatewayPath,
		HostSocketDevice: uint64(gatewayStatus.Dev),
		HostSocketInode:  gatewayStatus.Ino,
		SandboxSocket:    layout.SandboxSocket(),
	})
	if err != nil {
		t.Fatal(err)
	}

	runStorage, err := bwrap.OpenRunStorage(
		filepath.Join(base, "runs"),
		bwrap.RunStorageLimits{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer runStorage.Close()
	directories, err := runStorage.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	protectedRoots := nativeTestProtectedRoots(
		t,
		imageStorePath,
		persistentDataPath,
		hostRuntimePath,
		runStorage,
	)

	executable := filepath.Join(base, "toby")
	if err := os.WriteFile(executable, []byte("test executable"), 0o500); err != nil {
		t.Fatal(err)
	}
	tobyBinary, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer tobyBinary.Close()
	projectSource, err := os.Open(project.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer projectSource.Close()

	identity := bwrap.Identity{
		HostUID: os.Geteuid(),
		HostGID: os.Getegid(),
	}
	files := []toolfiles.File{
		{
			Owner:  "agent",
			Target: layout.Home + "/.config/agent/config.json",
			Data:   []byte("home-config"),
			Mode:   0o600,
			UID:    identity.HostUID,
			GID:    identity.HostGID,
		},
		{
			Owner:  "agent",
			Target: managedEntry.Target + "/AGENTS.md",
			Data:   []byte("managed-instructions"),
			Mode:   0o644,
			UID:    identity.HostUID,
			GID:    identity.HostGID,
		},
	}
	executor := &nativeRecordingExecutor{}
	native, err := NewNativeRun(t.Context(), NativeRunInput{
		Prepared:    image,
		Home:        home,
		Managed:     []ManagedDirectory{managed},
		Directories: directories,
		Projects: []NativeProject{{
			Input: bwrap.ProjectInput{
				Name:     project.Name,
				HostPath: project.HostPath,
			},
			Source: projectSource,
		}},
		ProtectedRoots:    protectedRoots,
		RuntimeRoot:       runtimeRoot,
		RuntimeAssets:     assetRegistry,
		SandboxGateway:    gateway,
		ToolFiles:         files,
		SandboxBinaryPath: executable,
		SandboxBinary:     tobyBinary,
		Workdir:           project.Target,
		Identity:          identity,
		ToolSandbox:       toolSandbox,
		Executor:          executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Close(); err != nil {
		t.Fatal(err)
	}
	assets := native.bubblewrapRun().Plan().RuntimeAssets
	if len(assets) != 2 ||
		assets[1].Target != layout.SandboxSocket() {
		t.Fatalf("native runtime assets = %#v", assets)
	}
	if _, err := os.Stat(assets[0].HostPath); err != nil {
		t.Fatalf("materialized runtime asset is unavailable before launch: %v", err)
	}

	runEnvironment := make(map[string]string)
	for _, variable := range native.bubblewrapRun().Plan().Environment {
		runEnvironment[variable.Name] = variable.Value
	}
	if runEnvironment["PATH"] != "/usr/local/bin:/usr/bin:/bin" ||
		runEnvironment["LANG"] != "C.UTF-8" ||
		runEnvironment["AGENT_MODEL"] != "test-model" {
		t.Fatalf("native run environment = %#v", runEnvironment)
	}
	if _, found := runEnvironment["HOME"]; found {
		t.Fatalf("native run overrides fixed HOME: %#v", runEnvironment)
	}

	assertNativeFile(
		t,
		filepath.Join(home.path, ".config", "agent", "config.json"),
		"home-config",
		0o600,
	)
	assertNativeFile(
		t,
		filepath.Join(managed.path, "AGENTS.md"),
		"managed-instructions",
		0o644,
	)

	if err := nativeLifecycle.Initialize(
		t.Context(),
		toolset,
		lifecycleContext,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := nativeLifecycle.Launch(
		t.Context(),
		toolset,
		primaryArgs,
	); err != nil {
		t.Fatal(err)
	}
	if executor.invocations != 3 {
		t.Fatalf("invocations = %d, want 3", executor.invocations)
	}

	runRoot := filepath.Dir(native.bubblewrapRun().Plan().Overlay.Upper)
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	if !image.closed || !home.closed || !managed.closed {
		t.Fatalf(
			"leases closed = image:%v home:%v managed:%v",
			image.closed,
			home.closed,
			managed.closed,
		)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run overlay remains after close: %v", err)
	}
}

func openNativeRunTestRoot(
	path string,
) (*safefs.Directory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return safefs.OpenDirectoryFile(
		file,
		path,
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
		},
	)
}

func nativeTestProtectedRoots(
	t *testing.T,
	imageStorePath string,
	persistentDataPath string,
	runtimePath string,
	runStorage *bwrap.RunStorage,
) bwrap.ProtectedRoots {
	t.Helper()

	imageStore, err := os.Open(imageStorePath)
	if err != nil {
		t.Fatal(err)
	}
	persistentData, err := os.Open(persistentDataPath)
	if err != nil {
		t.Fatal(errors.Join(err, imageStore.Close()))
	}
	runStorageRoot, err := runStorage.RootFile()
	if err != nil {
		t.Fatal(errors.Join(
			err,
			persistentData.Close(),
			imageStore.Close(),
		))
	}
	runtimeRoot, err := os.Open(runtimePath)
	if err != nil {
		t.Fatal(errors.Join(
			err,
			runStorageRoot.Close(),
			persistentData.Close(),
			imageStore.Close(),
		))
	}

	roots := bwrap.ProtectedRoots{
		ImageStore:     imageStore,
		PersistentData: persistentData,
		RunStorage:     runStorageRoot,
		Runtime:        runtimeRoot,
	}
	t.Cleanup(func() {
		for _, file := range []*os.File{
			roots.Runtime,
			roots.RunStorage,
			roots.PersistentData,
			roots.ImageStore,
		} {
			if err := file.Close(); err != nil &&
				!errors.Is(err, os.ErrInvalid) &&
				!errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		}
	})

	return roots
}

func assertNativeFile(
	t *testing.T,
	name string,
	want string,
	mode os.FileMode,
) {
	t.Helper()

	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		t.Fatalf("file %q mode = %v, want regular %04o", name, info.Mode(), mode)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("file %q data = %q, want %q", name, data, want)
	}
}
