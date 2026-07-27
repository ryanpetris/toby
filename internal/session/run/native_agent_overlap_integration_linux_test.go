//go:build linux

package run

// Exercises opt-in native-run overlap with real OpenCode and Codex processes,
// one shared private home, and independent projects, mounts, and overlays.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/config"
	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/toolfiles"
	codexconfig "petris.dev/toby/internal/tools/builtin/codex/config"
	opencodeconfig "petris.dev/toby/internal/tools/builtin/opencode/config"
)

const defaultNativeAgentOCIReference = "docker.io/library/node:24-bookworm-slim"

type nativeAgentIntegrationServices struct {
	base       string
	reference  string
	images     *oci.Store
	storage    *storage.Store
	runs       *bwrap.RunStorage
	executor   *bwrap.Executor
	binaryPath string
	binary     *os.File
	resolver   *os.File
	identity   bwrap.Identity
	roots      bwrap.ProtectedRoots
}

type nativeAgentRunSpec struct {
	name        string
	packageName string
	mounts      []nativeAgentMountSpec
}

type nativeAgentMountSpec struct {
	purpose string
	target  string
}

type nativeAgentIntegrationRun struct {
	native           *NativeRun
	plan             bwrap.Plan
	imageEnvironment []string
	files            []toolfiles.File
	visibilityPaths  []string
	spec             nativeAgentRunSpec
}

type nativeAgentProcess struct {
	cancel context.CancelFunc
	input  io.Closer
	done   chan struct{}
	stdout bytes.Buffer
	stderr bytes.Buffer
	code   int
	err    error
}

func TestNativeRunsKeepCodexUsableAfterSharedHomeOpenCodeCloses(
	t *testing.T,
) {
	if os.Getenv("TOBY_NATIVE_AGENT_OVERLAP_INTEGRATION") != "1" {
		t.Skip(
			"set TOBY_NATIVE_AGENT_OVERLAP_INTEGRATION=1 for native OpenCode/Codex overlap",
		)
	}

	services := prepareNativeAgentIntegrationServices(t)
	openCode := prepareNativeAgentIntegrationRun(t, services, nativeAgentRunSpec{
		name:        "opencode",
		packageName: "opencode-ai",
		mounts: []nativeAgentMountSpec{
			{
				purpose: "config",
				target:  layout.Home + "/.config/opencode",
			},
			{
				purpose: "data",
				target:  layout.Home + "/.local/share/opencode",
			},
		},
	})
	codex := prepareNativeAgentIntegrationRun(t, services, nativeAgentRunSpec{
		name:        "codex",
		packageName: "@openai/codex",
		mounts: []nativeAgentMountSpec{{
			purpose: "state",
			target:  layout.Home + "/.codex",
		}},
	})

	assertNativeAgentRunPlans(t, openCode, codex)
	assertNativeAgentGeneratedFile(t, openCode)
	assertNativeAgentGeneratedFile(t, codex)
	assertNativeAgentSandboxVisibility(t, openCode, codex)
	assertNativeAgentSandboxVisibility(t, codex, openCode)

	installNativeAgentClient(t, openCode)
	installNativeAgentClient(t, codex)

	const (
		openCodePIDNamespaceMarker = ".toby-opencode-pid-namespace"
		codexPIDNamespaceMarker    = ".toby-codex-pid-namespace"
	)
	port := availableNativeAgentPort(t)
	openCodeProcess := startNativeAgentProcess(
		t,
		openCode.native.bubblewrapRun(),
		openCodePIDNamespaceMarker,
		[]string{
			"opencode",
			"serve",
			"--hostname",
			"127.0.0.1",
			"--port",
			strconv.Itoa(port),
		},
		false,
	)
	codexProcess := startNativeAgentProcess(
		t,
		codex.native.bubblewrapRun(),
		codexPIDNamespaceMarker,
		[]string{"codex", "app-server"},
		true,
	)

	assertDistinctNativeAgentPIDNamespaces(
		t,
		openCode.plan.Home.HostPath,
		openCodePIDNamespaceMarker,
		openCodeProcess,
		codexPIDNamespaceMarker,
		codexProcess,
	)
	waitForNativeOpenCode(t, openCodeProcess, port)
	assertNativeAgentProcessesRunning(
		t,
		time.Second,
		openCodeProcess,
		codexProcess,
	)

	openCodeProcess.stop()
	waitForNativeAgentProcess(t, openCodeProcess, 15*time.Second)
	if err := openCode.native.Close(); err != nil {
		t.Fatal(err)
	}

	assertNativeAgentProcessesRunning(t, 500*time.Millisecond, codexProcess)

	codexProcess.stop()
	waitForNativeAgentProcess(t, codexProcess, 15*time.Second)
	assertNativeCodexVersion(t, codex.native.bubblewrapRun())
}

func prepareNativeAgentIntegrationServices(
	t *testing.T,
) *nativeAgentIntegrationServices {
	t.Helper()

	base := secureNativeAgentIntegrationPath(t)
	paths := config.Paths{
		Home:          filepath.Join(base, "host-home"),
		XDGCacheHome:  filepath.Join(base, "cache"),
		XDGDataHome:   filepath.Join(base, "data"),
		XDGConfigHome: filepath.Join(base, "config"),
		XDGRuntimeDir: filepath.Join(base, "runtime"),
	}
	for _, directory := range []string{paths.Home, paths.XDGRuntimeDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	images, err := oci.NewStore(
		paths,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := images.Close(); err != nil {
			t.Error(err)
		}
	})
	imageStoreRoot, err := images.ImageStoreFile()
	if err != nil {
		t.Fatal(err)
	}

	storageService, err := storage.NewStore(
		paths,
		storage.DefaultLimits(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storageService.Close(); err != nil {
			t.Error(err)
		}
	})
	persistentDataRoot, err := storageService.DataRootFile()
	if err != nil {
		t.Fatal(err)
	}

	runStorage, err := bwrap.OpenRunStorage(
		paths.RunStorageDir(),
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
	runStorageRoot, err := runStorage.RootFile()
	if err != nil {
		t.Fatal(err)
	}
	runtimePaths, err := paths.ResolveRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimePaths.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeRoot, err := os.Open(runtimePaths.Root)
	if err != nil {
		t.Fatal(err)
	}
	roots := bwrap.ProtectedRoots{
		ImageStore:     imageStoreRoot,
		PersistentData: persistentDataRoot,
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
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Open(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := binary.Close(); err != nil {
			t.Error(err)
		}
	})

	const resolverPath = "/etc/resolv.conf"
	resolver, err := os.Open(resolverPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resolver.Close(); err != nil {
			t.Error(err)
		}
	})

	reference := os.Getenv("TOBY_BWRAP_AGENT_OCI_REFERENCE")
	if reference == "" {
		reference = defaultNativeAgentOCIReference
	}

	return &nativeAgentIntegrationServices{
		base:       base,
		reference:  reference,
		images:     images,
		storage:    storageService,
		runs:       runStorage,
		executor:   executor,
		binaryPath: binaryPath,
		binary:     binary,
		resolver:   resolver,
		roots:      roots,
		identity: bwrap.Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
	}
}

func prepareNativeAgentIntegrationRun(
	t *testing.T,
	services *nativeAgentIntegrationServices,
	spec nativeAgentRunSpec,
) *nativeAgentIntegrationRun {
	t.Helper()

	prepareContext, cancelPrepare := context.WithTimeout(
		t.Context(),
		10*time.Minute,
	)
	defer cancelPrepare()
	ownershipTransferred := false
	prepared, err := services.images.Prepare(prepareContext, oci.Request{
		Reference: services.reference,
		Platform: ocispec.Platform{
			OS:           "linux",
			Architecture: runtime.GOARCH,
		},
		PullPolicy: image.PullIfMissing,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !ownershipTransferred {
			if err := prepared.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	imageEnvironment := append(
		[]string(nil),
		prepared.Spec().Runtime.Environment...,
	)
	rootfs, err := prepared.RootfsFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rootfs.Close(); err != nil {
			t.Error(err)
		}
	})
	rootfsSeed := storage.SeedSource{
		Root:            rootfs,
		RootDescription: prepared.RootfsPath(),
		ImagePath:       layout.Home,
	}

	home, err := services.storage.ResolveHome(
		prepareContext,
		"m3-native-agent-overlap",
		"default",
		rootfsSeed,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !ownershipTransferred {
			if err := home.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	projectPath := filepath.Join(services.base, "projects", spec.name)
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	project := bwrap.Project{
		Name:     spec.name,
		HostPath: projectPath,
		Target:   layout.Workspace + "/" + spec.name,
	}
	toolSandbox, err := bwrap.NewToolSandbox(bwrap.ToolSandboxOptions{
		Projects:         []bwrap.Project{project},
		ImageEnvironment: imageEnvironment,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range spec.mounts {
		if err := toolSandbox.AddMount(mount.Request{
			Key: mount.Key{
				Type:    mount.TypeTool,
				Name:    spec.name,
				Purpose: requested.purpose,
			},
			Target: requested.target,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if spec.name == "opencode" {
		if err := toolSandbox.SetEnvironment(
			t.Context(),
			"OPENCODE_CONFIG_DIR",
			filepath.Dir(opencodeconfig.NativePriorityConfigPath),
		); err != nil {
			t.Fatal(err)
		}
	}

	const resolverPath = "/etc/resolv.conf"
	if err := toolSandbox.AddBind(mount.Bind{
		HostPath: resolverPath,
		Target:   resolverPath,
		Access:   mount.AccessReadOnly,
	}); err != nil {
		t.Fatal(err)
	}

	managed, err := services.storage.ResolveManaged(
		prepareContext,
		storage.ProfileSelection{},
		toolSandbox.MountRequests(),
		nil,
		rootfsSeed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != len(spec.mounts) {
		t.Fatalf(
			"resolved managed directories = %d, want %d",
			len(managed),
			len(spec.mounts),
		)
	}
	t.Cleanup(func() {
		if !ownershipTransferred {
			for index := len(managed) - 1; index >= 0; index-- {
				if err := managed[index].Close(); err != nil {
					t.Error(err)
				}
			}
		}
	})

	projectMarker := ".toby-integration-" + spec.name + "-project"
	if err := os.WriteFile(
		filepath.Join(projectPath, projectMarker),
		[]byte(spec.name+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	visibilityPaths := []string{project.Target + "/" + projectMarker}
	for _, handle := range managed {
		entry := handle.Entry()
		marker := ".toby-integration-" + spec.name + "-" + entry.Key.Purpose
		if err := os.WriteFile(
			filepath.Join(entry.HostPath, marker),
			[]byte(entry.Key.String()+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		visibilityPaths = append(
			visibilityPaths,
			entry.Target+"/"+marker,
		)
	}

	projectSource, err := os.Open(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := projectSource.Close(); err != nil {
			t.Error(err)
		}
	})
	resolverParent, err := openNativeDirectory(
		filepath.Dir(services.resolver.Name()),
		"resolver bind parent",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resolverParent.Close(); err != nil {
			t.Error(err)
		}
	})

	binds := toolSandbox.Binds()
	if len(binds) != 1 {
		t.Fatalf("resolved external binds = %d, want 1", len(binds))
	}
	files, err := nativeAgentToolFiles(spec, services.identity)
	if err != nil {
		t.Fatal(err)
	}
	managedInputs := make([]ManagedDirectory, len(managed))
	for index := range managed {
		managedInputs[index] = managed[index]
	}
	directories, err := services.runs.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	native, err := NewNativeRun(t.Context(), NativeRunInput{
		Prepared:    prepared,
		Home:        home,
		Managed:     managedInputs,
		Directories: directories,
		Projects: []NativeProject{{
			Input: bwrap.ProjectInput{
				Name:     project.Name,
				HostPath: project.HostPath,
			},
			Source: projectSource,
		}},
		Binds: []NativeBind{{
			Bind:   binds[0],
			Source: services.resolver,
			Parent: resolverParent,
		}},
		ProtectedRoots:    services.roots,
		ToolFiles:         files,
		SandboxBinaryPath: services.binaryPath,
		SandboxBinary:     services.binary,
		Workdir:           project.Target,
		Identity:          services.identity,
		ToolSandbox:       toolSandbox,
		Executor:          services.executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownershipTransferred = true
	t.Cleanup(func() {
		if err := native.Close(); err != nil {
			t.Error(err)
		}
	})

	plan := native.bubblewrapRun().Plan()
	if len(plan.GeneratedFiles) != len(files) {
		t.Fatalf(
			"generated files = %d, want %d",
			len(plan.GeneratedFiles),
			len(files),
		)
	}

	return &nativeAgentIntegrationRun{
		native:           native,
		plan:             plan,
		imageEnvironment: imageEnvironment,
		files:            files,
		visibilityPaths:  visibilityPaths,
		spec:             spec,
	}
}

func nativeAgentToolFiles(
	spec nativeAgentRunSpec,
	identity bwrap.Identity,
) ([]toolfiles.File, error) {
	ownership := toolfiles.Ownership{
		UID: identity.HostUID,
		GID: identity.HostGID,
	}
	cfg := sessionconfig.Config{
		Instructions: sessionconfig.Instructions{
			Contents: [][]byte{
				[]byte("# Native agent overlap integration\n"),
			},
		},
	}

	switch spec.name {
	case "opencode":
		return opencodeconfig.NativeFiles(spec.name, ownership, cfg)
	case "codex":
		return codexconfig.NativeFiles(spec.name, ownership, cfg)
	default:
		return nil, errors.New("unsupported native agent fixture " + spec.name)
	}
}

func installNativeAgentClient(
	t *testing.T,
	agent *nativeAgentIntegrationRun,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, err := agent.native.bubblewrapRun().Execute(ctx, bwrap.Command{
		Argv: []string{
			"npm",
			"install",
			"--global",
			"--no-audit",
			"--no-fund",
			"--cache",
			"/tmp/npm-cache",
			agent.spec.packageName,
		},
		Mode:         bwrap.ExecutionNonInteractive,
		Root:         true,
		Capabilities: bwrap.CapabilityRootLifecycle,
	}, bwrap.ProcessIO{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf(
			"install %s: code=%d error=%v stdout=%s stderr=%s",
			agent.spec.packageName,
			code,
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
}

func assertNativeAgentSandboxVisibility(
	t *testing.T,
	own *nativeAgentIntegrationRun,
	peer *nativeAgentIntegrationRun,
) {
	t.Helper()

	argv := []string{
		"/bin/sh",
		"-c",
		agentOverlapCheckScriptFixture,
		"native-visibility",
	}
	argv = append(argv, own.visibilityPaths...)
	argv = append(argv, "--")
	argv = append(argv, peer.visibilityPaths...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, err := own.native.bubblewrapRun().Execute(t.Context(), bwrap.Command{
		Argv:         argv,
		Mode:         bwrap.ExecutionNonInteractive,
		Capabilities: bwrap.CapabilityDropAll,
	}, bwrap.ProcessIO{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf(
			"%s sandbox visibility: code=%d error=%v stdout=%s stderr=%s",
			own.spec.name,
			code,
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
}

func startNativeAgentProcess(
	t *testing.T,
	run *bwrap.Run,
	pidNamespaceMarker string,
	argv []string,
	keepStdinOpen bool,
) *nativeAgentProcess {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	process := &nativeAgentProcess{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	streams := bwrap.ProcessIO{
		Stdout: &process.stdout,
		Stderr: &process.stderr,
	}
	if keepStdinOpen {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		streams.Stdin = reader
		process.input = writer
		t.Cleanup(func() {
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	commandArgv := []string{
		"/bin/sh",
		"-c",
		agentNamespaceRecordScriptFixture,
		"native-agent",
		pidNamespaceMarker,
	}
	commandArgv = append(commandArgv, argv...)
	go func() {
		defer close(process.done)
		process.code, process.err = run.Execute(ctx, bwrap.Command{
			Argv:         commandArgv,
			Mode:         bwrap.ExecutionNonInteractive,
			Capabilities: bwrap.CapabilityDropAll,
		}, streams)
	}()
	t.Cleanup(func() {
		process.stop()
		select {
		case <-process.done:
		case <-time.After(15 * time.Second):
			t.Errorf("timed out stopping %q", argv[0])
		}
	})

	return process
}

func assertDistinctNativeAgentPIDNamespaces(
	t *testing.T,
	homePath string,
	openCodeMarker string,
	openCode *nativeAgentProcess,
	codexMarker string,
	codex *nativeAgentProcess,
) {
	t.Helper()

	type marker struct {
		name      string
		path      string
		process   *nativeAgentProcess
		namespace string
	}
	markers := []*marker{
		{
			name:    "OpenCode",
			path:    filepath.Join(homePath, openCodeMarker),
			process: openCode,
		},
		{
			name:    "Codex",
			path:    filepath.Join(homePath, codexMarker),
			process: codex,
		},
	}

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		complete := true
		for _, marker := range markers {
			if marker.namespace != "" {
				continue
			}
			data, err := os.ReadFile(marker.path)
			if err == nil {
				fields := strings.Fields(string(data))
				if len(fields) == 2 &&
					strings.HasPrefix(fields[1], "pid:[") {
					marker.namespace = fields[1]
					continue
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			complete = false

			select {
			case <-marker.process.done:
				t.Fatalf(
					"%s exited before publishing its PID namespace: code=%d error=%v stdout=%s stderr=%s",
					marker.name,
					marker.process.code,
					marker.process.err,
					strings.TrimSpace(marker.process.stdout.String()),
					strings.TrimSpace(marker.process.stderr.String()),
				)
			default:
			}
		}
		if complete {
			if markers[0].namespace == markers[1].namespace {
				t.Fatalf(
					"native agents share PID namespace %q",
					markers[0].namespace,
				)
			}
			return
		}

		select {
		case <-deadline.C:
			var diagnostics []string
			for _, marker := range markers {
				data, readErr := os.ReadFile(marker.path)
				processState := "running"
				select {
				case <-marker.process.done:
					processState = fmt.Sprintf(
						"exited code=%d error=%v stdout=%q stderr=%q",
						marker.process.code,
						marker.process.err,
						strings.TrimSpace(marker.process.stdout.String()),
						strings.TrimSpace(marker.process.stderr.String()),
					)
				default:
				}
				diagnostics = append(
					diagnostics,
					fmt.Sprintf(
						"%s path=%q data=%q read_error=%v process=%s",
						marker.name,
						marker.path,
						strings.TrimSpace(string(data)),
						readErr,
						processState,
					),
				)
			}
			t.Fatalf(
				"timed out waiting for native agent PID namespaces: %s",
				strings.Join(diagnostics, "; "),
			)
		case <-ticker.C:
		}
	}
}

func (p *nativeAgentProcess) stop() {
	p.cancel()
	if p.input != nil {
		_ = p.input.Close()
	}
}

func waitForNativeAgentProcess(
	t *testing.T,
	process *nativeAgentProcess,
	timeout time.Duration,
) {
	t.Helper()

	select {
	case <-process.done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for native agent process")
	}
}

func waitForNativeOpenCode(
	t *testing.T,
	process *nativeAgentProcess,
	port int,
) {
	t.Helper()

	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	for {
		connection, err := net.DialTimeout("tcp4", address, 200*time.Millisecond)
		if err == nil {
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}

		select {
		case <-process.done:
			t.Fatalf(
				"OpenCode exited before listening: code=%d error=%v stdout=%s stderr=%s",
				process.code,
				process.err,
				strings.TrimSpace(process.stdout.String()),
				strings.TrimSpace(process.stderr.String()),
			)
		case <-deadline.C:
			t.Fatalf(
				"OpenCode did not listen on %s before the readiness deadline",
				address,
			)
		case <-ticker.C:
		}
	}
}

func assertNativeAgentProcessesRunning(
	t *testing.T,
	duration time.Duration,
	processes ...*nativeAgentProcess,
) {
	t.Helper()

	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		for _, process := range processes {
			select {
			case <-process.done:
				t.Fatalf(
					"native agent exited during overlap: code=%d error=%v stdout=%s stderr=%s",
					process.code,
					process.err,
					strings.TrimSpace(process.stdout.String()),
					strings.TrimSpace(process.stderr.String()),
				)
			default:
			}
		}

		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func assertNativeAgentRunPlans(
	t *testing.T,
	openCode *nativeAgentIntegrationRun,
	codex *nativeAgentIntegrationRun,
) {
	t.Helper()

	if openCode.plan.Home != codex.plan.Home {
		t.Fatal("native agent runs do not share one private home")
	}
	if openCode.plan.Overlay.Upper == codex.plan.Overlay.Upper ||
		openCode.plan.Overlay.Work == codex.plan.Overlay.Work {
		t.Fatal("native agent runs share an overlay directory")
	}
	if len(openCode.plan.Projects) != 1 || len(codex.plan.Projects) != 1 {
		t.Fatal("native agent runs must each have one project")
	}
	openCodeProject := openCode.plan.Projects[0]
	codexProject := codex.plan.Projects[0]
	if openCodeProject.Name == codexProject.Name ||
		openCodeProject.HostPath == codexProject.HostPath ||
		openCodeProject.Target == codexProject.Target {
		t.Fatal("native agent runs do not have distinct projects")
	}
	if len(openCode.plan.ManagedDirectories) != len(openCode.spec.mounts) ||
		len(codex.plan.ManagedDirectories) != len(codex.spec.mounts) {
		t.Fatal("native agent run managed-directory count does not match its tool")
	}
	allMounts := append(
		append(
			[]mount.Entry(nil),
			openCode.plan.ManagedDirectories...,
		),
		codex.plan.ManagedDirectories...,
	)
	for index, entry := range allMounts {
		for earlier := range index {
			other := allMounts[earlier]
			if entry.Key == other.Key ||
				entry.HostPath == other.HostPath ||
				entry.Target == other.Target {
				t.Fatal("native agent runs do not have distinct managed directories")
			}
		}
	}
	for _, agent := range []*nativeAgentIntegrationRun{openCode, codex} {
		for _, requested := range agent.spec.mounts {
			if !nativeAgentPlanHasMountTarget(agent.plan, requested.target) {
				t.Fatalf(
					"%s plan lacks managed target %q",
					agent.spec.name,
					requested.target,
				)
			}
		}
	}

	openCodePATH, found := imageEnvironmentValue(
		openCode.imageEnvironment,
		"PATH",
	)
	if !found || openCodePATH == "" {
		t.Fatal("OpenCode OCI image does not declare PATH")
	}
	codexPATH, found := imageEnvironmentValue(codex.imageEnvironment, "PATH")
	if !found || codexPATH == "" {
		t.Fatal("Codex OCI image does not declare PATH")
	}
	if got, found := planEnvironmentValue(openCode.plan, "PATH"); !found ||
		got != openCodePATH {
		t.Fatalf("OpenCode run PATH = %q, want OCI PATH %q", got, openCodePATH)
	}
	if got, found := planEnvironmentValue(codex.plan, "PATH"); !found ||
		got != codexPATH {
		t.Fatalf("Codex run PATH = %q, want OCI PATH %q", got, codexPATH)
	}
	wantOpenCodeConfigDir := filepath.Dir(
		opencodeconfig.NativePriorityConfigPath,
	)
	if got, found := planEnvironmentValue(
		openCode.plan,
		"OPENCODE_CONFIG_DIR",
	); !found || got != wantOpenCodeConfigDir {
		t.Fatalf(
			"OpenCode config directory = %q, want %q",
			got,
			wantOpenCodeConfigDir,
		)
	}
	if _, found := planEnvironmentValue(codex.plan, "OPENCODE_CONFIG_DIR"); found {
		t.Fatal("Codex run unexpectedly has OpenCode's config-directory override")
	}
}

func assertNativeAgentGeneratedFile(
	t *testing.T,
	agent *nativeAgentIntegrationRun,
) {
	t.Helper()

	for _, expected := range agent.files {
		generated, found := nativeAgentGeneratedFile(
			agent.plan,
			expected.Target,
		)
		if !found {
			t.Fatalf(
				"%s generated files lack %q",
				agent.spec.name,
				expected.Target,
			)
		}
		info, err := os.Lstat(generated.HostPath)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != expected.Mode {
			t.Fatalf(
				"%s generated file %q mode = %v, want regular %04o",
				agent.spec.name,
				expected.Target,
				info.Mode(),
				expected.Mode,
			)
		}
		data, err := os.ReadFile(generated.HostPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected.Data) {
			t.Fatalf(
				"%s generated file %q differs from its native renderer",
				agent.spec.name,
				expected.Target,
			)
		}
	}
}

func nativeAgentPlanHasMountTarget(plan bwrap.Plan, target string) bool {
	for _, entry := range plan.ManagedDirectories {
		if entry.Target == target {
			return true
		}
	}

	return false
}

func nativeAgentGeneratedFile(
	plan bwrap.Plan,
	target string,
) (bwrap.GeneratedFile, bool) {
	for _, file := range plan.GeneratedFiles {
		if file.Target == target {
			return file, true
		}
	}

	return bwrap.GeneratedFile{}, false
}

func assertNativeCodexVersion(t *testing.T, run *bwrap.Run) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, err := run.Execute(ctx, bwrap.Command{
		Argv:         []string{"codex", "--version"},
		Mode:         bwrap.ExecutionNonInteractive,
		Capabilities: bwrap.CapabilityDropAll,
	}, bwrap.ProcessIO{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf(
			"Codex after OpenCode close: code=%d error=%v stdout=%s stderr=%s",
			code,
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("Codex returned no version after the OpenCode native run closed")
	}
}

func availableNativeAgentPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

func secureNativeAgentIntegrationPath(t *testing.T) string {
	t.Helper()

	path, err := os.MkdirTemp(".", ".toby-native-agent-overlap-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Error(err)
		}
	})

	return absolute
}

func imageEnvironmentValue(
	environment []string,
	name string,
) (string, bool) {
	var result string
	var found bool
	for _, entry := range environment {
		entryName, value, valid := strings.Cut(entry, "=")
		if valid && entryName == name {
			result = value
			found = true
		}
	}

	return result, found
}

func planEnvironmentValue(
	plan bwrap.Plan,
	name string,
) (string, bool) {
	for _, entry := range plan.Environment {
		if entry.Name == name {
			return entry.Value, true
		}
	}

	return "", false
}
