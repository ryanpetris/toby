//go:build linux

package docker

// Exercises the selected built-in Docker tool through lifecycle relay
// collection, a real Bubblewrap run, and a fake Docker-compatible Unix
// endpoint without granting access to a host Docker control.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/oci"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/bwrap"
	sessionrun "petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/storage/safefs"
	"petris.dev/toby/internal/tools"
)

const (
	dockerRelayIntegrationEnvironment = "TOBY_DOCKER_RELAY_INTEGRATION"
	dockerIntegrationVersion          = "fake-engine-1.0"
	dockerIntegrationAPIVersion       = "1.47"
)

type dockerIntegrationImage struct {
	path   string
	spec   oci.Spec
	closed bool
}

var _ sessionrun.PreparedImage = (*dockerIntegrationImage)(nil)

func (i *dockerIntegrationImage) RootfsPath() string {
	if i == nil || i.closed {
		return ""
	}

	return i.path
}

func (i *dockerIntegrationImage) RootfsFile() (*os.File, error) {
	if i == nil || i.closed {
		return nil, os.ErrClosed
	}

	return os.Open(i.path)
}

func (i *dockerIntegrationImage) Spec() oci.Spec {
	if i == nil {
		return oci.Spec{}
	}

	return i.spec
}

func (i *dockerIntegrationImage) Close() error {
	if i != nil {
		i.closed = true
	}

	return nil
}

type dockerIntegrationHome struct {
	path     string
	identity storage.HomeIdentity
	closed   bool
}

var _ sessionrun.PrivateHome = (*dockerIntegrationHome)(nil)

func (h *dockerIntegrationHome) Identity() storage.HomeIdentity {
	if h == nil {
		return storage.HomeIdentity{}
	}

	return h.identity
}

func (h *dockerIntegrationHome) HostPath() string {
	if h == nil || h.closed {
		return ""
	}

	return h.path
}

func (h *dockerIntegrationHome) File() (*os.File, error) {
	if h == nil || h.closed {
		return nil, os.ErrClosed
	}

	return os.Open(h.path)
}

func (h *dockerIntegrationHome) Close() error {
	if h != nil {
		h.closed = true
	}

	return nil
}

type dockerIntegrationEndpoint struct {
	path     string
	listener *net.UnixListener
	server   *http.Server
	done     chan error

	mu       sync.Mutex
	requests []string
}

var _ io.Closer = (*dockerIntegrationEndpoint)(nil)

func TestDockerToolLaunchesThroughBubblewrapSocketRelay(t *testing.T) {
	if os.Getenv(dockerRelayIntegrationEnvironment) != "1" {
		t.Skip(
			"set TOBY_DOCKER_RELAY_INTEGRATION=1 to run the Bubblewrap Docker relay integration",
		)
	}

	base := secureDockerIntegrationPath(t)
	hostHome := filepath.Join(base, "host-home")
	hostDockerConfig := filepath.Join(hostHome, ".docker")
	persistentDataPath := filepath.Join(base, "persistent-data")
	imageStorePath := filepath.Join(persistentDataPath, "images")
	homeIdentity, err := storage.ResolveHomeIdentity(
		"docker-integration",
		"default",
	)
	if err != nil {
		t.Fatal(err)
	}
	privateHome := filepath.Join(
		persistentDataPath,
		"volumes",
		homeIdentity.ID,
		"_data",
	)
	rootfsPath := filepath.Join(imageStorePath, "rootfs")
	runtimePath := filepath.Join(base, "runtime")
	for _, directory := range []string{
		hostHome,
		hostDockerConfig,
		privateHome,
		filepath.Join(rootfsPath, "usr", "bin"),
		runtimePath,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(hostDockerConfig, "config.json"),
		[]byte("host-docker-config\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	buildDockerIntegrationCLI(t, rootfsPath)

	endpoint := startDockerIntegrationEndpoint(
		t,
		filepath.Join(t.TempDir(), "docker.sock"),
	)
	t.Cleanup(func() {
		if err := endpoint.Close(); err != nil {
			t.Error(err)
		}
	})

	runStoragePath := filepath.Join(base, "runs")
	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	imageEnvironment := []string{
		"DOCKER_CONTEXT=inherited-context",
		"PATH=/usr/bin",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	toolSandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{
		ImageEnvironment: imageEnvironment,
		ForegroundStreams: bwrap.ProcessIO{
			Stdout: &stdout,
			Stderr: &stderr,
		},
		StartLifecycleOperation: func(
			context.Context,
			[]string,
			sandboxapi.ExecOptions,
		) (bwrap.ProcessIO, func(error)) {
			return bwrap.ProcessIO{
				Stdout: &stdout,
				Stderr: &stderr,
			}, nil
		},
		ForegroundMode: bwrap.ExecutionNonInteractive,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := provide(
		config.Paths{Home: hostHome},
		toolSandbox,
		nil,
	).Service
	service.(*dockerTool).socket = endpoint.path

	registry, err := tools.NewRegistry([]tools.Tool{service})
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := registry.Build([]string{Name}, Name)
	if err != nil {
		t.Fatal(err)
	}
	orderedTools := toolset.OrderedTools()
	if toolset.Primary() != service ||
		len(orderedTools) != 1 ||
		orderedTools[0] != service {
		t.Fatalf("selected Docker toolset = %#v", orderedTools)
	}

	nativeLifecycle := lifecycle.NewRunner(
		status.NewService(nil),
	)
	lifecycleContext := lifecycle.Context{Options: &tools.Options{}}
	if err := nativeLifecycle.PrepareHost(
		t.Context(),
		toolset,
		lifecycleContext,
	); err != nil {
		t.Fatal(err)
	}
	if err := nativeLifecycle.Configure(
		t.Context(),
		toolset,
		lifecycleContext,
		[]string{
			"version",
			"--format",
			"{{.Server.Version}}",
		},
	); err != nil {
		t.Fatal(err)
	}
	if host, found := toolSandbox.Environment("DOCKER_HOST"); !found ||
		host != "unix://"+sandboxSocketPath {
		t.Fatalf("DOCKER_HOST = %q, found %v", host, found)
	}
	if contextName, found := toolSandbox.Environment(
		"DOCKER_CONTEXT",
	); found {
		t.Fatalf("DOCKER_CONTEXT = %q, want absent", contextName)
	}
	configuredBinds := toolSandbox.Binds()
	if len(configuredBinds) != 1 {
		t.Fatalf("Docker bind declarations = %#v, want host configuration", configuredBinds)
	}
	dockerConfigSource, err := os.Open(configuredBinds[0].HostPath)
	if err != nil {
		t.Fatal(err)
	}
	dockerConfigParent, err := os.Open(filepath.Dir(configuredBinds[0].HostPath))
	if err != nil {
		dockerConfigSource.Close()
		t.Fatal(err)
	}
	nativeBinds := []sessionrun.NativeBind{{
		Bind:   configuredBinds[0],
		Source: dockerConfigSource,
		Parent: dockerConfigParent,
	}}

	relayRegistry, err := lifecycle.SocketRelays(toolset)
	if err != nil {
		t.Fatal(err)
	}
	runStorage, err := bwrap.OpenRunStorage(
		runStoragePath,
		bwrap.RunStorageLimits{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runStorage.Close(); err != nil {
			t.Error(err)
		}
	})
	protectedRoots := dockerIntegrationProtectedRoots(
		t,
		imageStorePath,
		persistentDataPath,
		runtimePath,
		runStorage,
	)
	directories, err := runStorage.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directories.Close(); err != nil {
			t.Error(err)
		}
	})

	runtimeRoot, err := dockerIntegrationRuntimeRoot(directories)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtimeRoot.Close(); err != nil {
			t.Error(err)
		}
	})
	socketRelays, err := relayRegistry.Start(
		t.Context(),
		runtimeRoot,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := socketRelays.Close(); err != nil {
			t.Error(err)
		}
	})
	identity := bwrap.Identity{
		HostUID: os.Geteuid(),
		HostGID: os.Getegid(),
	}

	image := &dockerIntegrationImage{
		path: rootfsPath,
		spec: oci.Spec{
			Manifest: ocispec.Descriptor{
				Digest: digest.FromString(
					"toby Docker relay integration rootfs",
				),
			},
			Runtime: oci.RuntimeConfig{
				Environment: imageEnvironment,
			},
		},
	}
	t.Cleanup(func() {
		if err := image.Close(); err != nil {
			t.Error(err)
		}
	})
	home := &dockerIntegrationHome{
		path:     privateHome,
		identity: homeIdentity,
	}
	t.Cleanup(func() {
		if err := home.Close(); err != nil {
			t.Error(err)
		}
	})
	tobyPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tobyBinary, err := os.Open(tobyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tobyBinary.Close(); err != nil &&
			!errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})

	native, err := sessionrun.NewNativeRun(
		t.Context(),
		sessionrun.NativeRunInput{
			Prepared:          image,
			Home:              home,
			Binds:             nativeBinds,
			Directories:       directories,
			SocketRelays:      socketRelays,
			ProtectedRoots:    protectedRoots,
			SandboxBinaryPath: tobyPath,
			SandboxBinary:     tobyBinary,
			Workdir:           "/",
			Identity:          identity,
			ToolSandbox:       toolSandbox,
			Executor:          executor,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := native.Close(); err != nil {
			t.Error(err)
		}
	})

	if err := nativeLifecycle.Initialize(
		t.Context(),
		toolset,
		lifecycleContext,
		false,
	); err != nil {
		t.Fatalf("initialize Docker tool: %v\nstderr:\n%s", err, stderr.String())
	}
	if err := nativeLifecycle.Launch(
		t.Context(),
		toolset,
		[]string{
			"version",
			"--format",
			"{{.Server.Version}}",
		},
	); err != nil {
		t.Fatalf("launch Docker tool: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := stdout.String(); got != dockerIntegrationVersion+"\n" {
		t.Fatalf("Docker output = %q\nstderr:\n%s", got, stderr.String())
	}
	if got := endpoint.Requests(); !reflect.DeepEqual(
		got,
		[]string{
			"/_ping",
			"/v" + dockerIntegrationAPIVersion + "/version",
		},
	) {
		t.Fatalf("Docker endpoint requests = %#v", got)
	}

	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	if !image.closed || !home.closed {
		t.Fatalf(
			"run ownership closed = image:%v home:%v",
			image.closed,
			home.closed,
		)
	}
	info, err := os.Stat(endpoint.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("fake host Docker endpoint mode = %v", info.Mode())
	}
}

func secureDockerIntegrationPath(t *testing.T) string {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(
		workingDirectory,
		".toby-docker-relay-",
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

	return base
}

func dockerIntegrationProtectedRoots(
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

func buildDockerIntegrationCLI(t *testing.T, rootfsPath string) {
	t.Helper()

	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dockerPath := filepath.Join(rootfsPath, "usr", "bin", "docker")
	var output bytes.Buffer
	command := exec.CommandContext(
		t.Context(),
		goExecutable,
		"build",
		"-trimpath",
		"-o",
		dockerPath,
		"./testdata/dockercli",
	)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build fake Docker CLI: %v\n%s", err, output.String())
	}
	if err := os.Link(
		dockerPath,
		filepath.Join(rootfsPath, "usr", "bin", "which"),
	); err != nil {
		t.Fatal(err)
	}
}

func dockerIntegrationRuntimeRoot(
	directories *bwrap.RunDirectories,
) (*safefs.Directory, error) {
	file, err := directories.RuntimeFile()
	if err != nil {
		return nil, err
	}
	root, openErr := safefs.OpenDirectoryFile(
		file,
		directories.RuntimePath(),
		safefs.DirectoryOptions{
			OwnerUID: os.Geteuid(),
			OwnerGID: os.Getegid(),
		},
	)
	closeErr := file.Close()
	if openErr != nil {
		return nil, errors.Join(openErr, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(closeErr, root.Close())
	}

	return root, nil
}

func startDockerIntegrationEndpoint(
	t *testing.T,
	socketPath string,
) *dockerIntegrationEndpoint {
	t.Helper()

	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: socketPath, Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(errors.Join(err, listener.Close()))
	}

	result := &dockerIntegrationEndpoint{
		path:     socketPath,
		listener: listener,
		done:     make(chan error, 1),
	}
	result.server = &http.Server{Handler: http.HandlerFunc(result.handle)}
	go func() {
		result.done <- result.server.Serve(listener)
	}()

	return result
}

func (e *dockerIntegrationEndpoint) handle(
	response http.ResponseWriter,
	request *http.Request,
) {
	e.mu.Lock()
	e.requests = append(e.requests, request.URL.Path)
	e.mu.Unlock()

	switch request.URL.Path {
	case "/_ping":
		response.Header().Set(
			"API-Version",
			dockerIntegrationAPIVersion,
		)
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("OK"))
	case "/v" + dockerIntegrationAPIVersion + "/version":
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			response,
			`{"Version":%q,"ApiVersion":%q}`,
			dockerIntegrationVersion,
			dockerIntegrationAPIVersion,
		)
	default:
		http.NotFound(response, request)
	}
}

func (e *dockerIntegrationEndpoint) Requests() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.requests...)
}

func (e *dockerIntegrationEndpoint) Close() error {
	if e == nil || e.server == nil {
		return nil
	}

	closeErr := e.server.Close()
	serveErr := <-e.done
	e.server = nil
	e.listener = nil
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}

	return errors.Join(closeErr, serveErr)
}
