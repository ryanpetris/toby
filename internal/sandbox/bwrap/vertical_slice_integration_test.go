//go:build linux

package bwrap

// Exercises the complete opt-in Linux vertical slice against a real OCI
// rootfs, native persistent directories, and Bubblewrap with no Docker runtime.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage"
)

const defaultVerticalOCIReference = "docker.io/library/debian:bookworm-slim"

type verticalFixture struct {
	run        *Run
	prepared   *oci.Prepared
	home       *storage.HomeHandle
	managed    *storage.ManagedHandle
	project    string
	readOnly   string
	execFD     int
	statusFD   int
	executable string
	sources    Sources
}

func TestBubblewrapVerticalSliceUsesNativeStateAndSequentialOverlay(
	t *testing.T,
) {
	if os.Getenv("TOBY_BWRAP_VERTICAL_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_VERTICAL_INTEGRATION=1 on the target Linux host")
	}
	reference := os.Getenv("TOBY_BWRAP_VERTICAL_OCI_REFERENCE")
	if reference == "" {
		reference = defaultVerticalOCIReference
	}
	fixture := prepareVerticalFixture(t, reference, false)

	root := Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			verticalFirstRunScriptFixture,
			"root-fd-check",
			strconv.Itoa(fixture.execFD),
		},
		Mode:         ExecutionNonInteractive,
		Root:         true,
		Capabilities: CapabilityRootLifecycle,
	}
	if code, err := fixture.run.Execute(
		t.Context(),
		root,
		ProcessIO{Stderr: os.Stderr},
	); err != nil || code != 0 {
		t.Fatalf("root setup: code=%d error=%v", code, err)
	}

	rootFollowup := Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			verticalSecondRunScriptFixture,
		},
		Mode:         ExecutionNonInteractive,
		Root:         true,
		Capabilities: CapabilityRootLifecycle,
	}
	if code, err := fixture.run.Execute(
		t.Context(),
		rootFollowup,
		ProcessIO{Stderr: os.Stderr},
	); err != nil || code != 0 {
		t.Fatalf(
			"follow-up root setup: code=%d error=%v",
			code,
			err,
		)
	}

	pinnedNamespace := pinCompletedOverlayMount(t, fixture)
	released := make(chan error, 1)
	go func() {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		released <- pinnedNamespace.Close()
	}()

	var output bytes.Buffer
	application := Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			verticalThirdRunScriptFixture,
			"application-fd-check",
			strconv.Itoa(fixture.execFD),
			strconv.Itoa(fixture.statusFD),
		},
		Mode:         ExecutionManagedPTY,
		Capabilities: CapabilityDropAll,
	}
	started := time.Now()
	code, err := fixture.run.Execute(
		t.Context(),
		application,
		ProcessIO{Stdout: &output},
	)
	elapsed := time.Since(started)
	releaseErr := <-released
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if err != nil || code != 0 {
		t.Fatalf(
			"application: code=%d error=%v output=%q",
			code,
			err,
			output.String(),
		)
	}
	if !strings.Contains(output.String(), "vertical-ok") {
		t.Fatalf("application output = %q", output.String())
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf(
			"application completed in %s while the previous overlay namespace was pinned",
			elapsed,
		)
	}
	if strings.Contains(output.String(), "bwrap:") {
		t.Fatalf(
			"successful overlay reuse exposed discarded setup diagnostics: %q",
			output.String(),
		)
	}

	assertFileContent(
		t,
		filepath.Join(fixture.home.HostPath(), "native.txt"),
		"home-native",
	)
	assertFileContent(
		t,
		filepath.Join(fixture.managed.Entry().HostPath, "value"),
		"managed-native",
	)
	assertFileContent(
		t,
		filepath.Join(fixture.project, "value"),
		"project-native",
	)
	if _, err := os.Stat(filepath.Join(fixture.readOnly, "value")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("read-only project was modified: %v", err)
	}
	if _, err := os.Stat(
		filepath.Join(fixture.prepared.RootfsPath(), "m2-root-marker"),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("immutable lower was modified: %v", err)
	}

	status := Command{
		Argv:         []string{"/bin/sh", "-c", "exit 37"},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}
	if code, err := fixture.run.Execute(
		t.Context(),
		status,
		ProcessIO{},
	); err != nil || code != 37 {
		t.Fatalf("status command: code=%d error=%v", code, err)
	}
}

func TestBubblewrapVerticalSliceLaunchesOpenCodeAndCodexWithoutAgent(
	t *testing.T,
) {
	if os.Getenv("TOBY_BWRAP_AGENT_INTEGRATION") != "1" {
		t.Skip("set TOBY_BWRAP_AGENT_INTEGRATION=1 for OpenCode/Codex smoke")
	}
	reference := os.Getenv("TOBY_BWRAP_AGENT_OCI_REFERENCE")
	if reference == "" {
		reference = "docker.io/library/node:24-bookworm-slim"
	}
	fixture := prepareVerticalFixture(t, reference, true)

	var installOutput bytes.Buffer
	install := Command{
		Argv: []string{
			"/bin/sh",
			"-c",
			"npm install --global --no-audit --no-fund opencode-ai @openai/codex",
		},
		Mode:         ExecutionNonInteractive,
		Root:         true,
		Capabilities: CapabilityRootLifecycle,
	}
	code, err := fixture.run.Execute(t.Context(), install, ProcessIO{
		Stdout: &installOutput,
		Stderr: &installOutput,
	})
	if err != nil || code != 0 {
		t.Fatalf(
			"install agents: code=%d error=%v output=%s",
			code,
			err,
			installOutput.String(),
		)
	}

	for _, executable := range []string{"opencode", "codex"} {
		var output bytes.Buffer
		command := Command{
			Argv:         []string{executable, "--version"},
			Mode:         ExecutionNonInteractive,
			Capabilities: CapabilityDropAll,
		}
		code, err := fixture.run.Execute(t.Context(), command, ProcessIO{
			Stdout: &output,
			Stderr: &output,
		})
		if err != nil || code != 0 {
			t.Fatalf(
				"%s smoke: code=%d error=%v output=%s",
				executable,
				code,
				err,
				output.String(),
			)
		}
		if strings.TrimSpace(output.String()) == "" {
			t.Fatalf("%s returned no version output", executable)
		}
		t.Logf("%s: %s", executable, strings.TrimSpace(output.String()))
	}
}

func prepareVerticalFixture(
	t *testing.T,
	reference string,
	withDNS bool,
) *verticalFixture {
	t.Helper()

	return prepareVerticalFixtureWithManaged(
		t,
		reference,
		withDNS,
		mount.Request{
			Key: mount.Key{
				Type:    mount.TypeTool,
				Name:    "vertical",
				Purpose: "state",
			},
			Target: layout.Home + "/.state",
			Access: mount.AccessRegular,
		},
	)
}

func prepareVerticalFixtureWithManaged(
	t *testing.T,
	reference string,
	withDNS bool,
	managedRequest mount.Request,
) *verticalFixture {
	t.Helper()

	base := secureVerticalPath(t)
	paths := config.Paths{
		Home:          filepath.Join(base, "host-home"),
		XDGCacheHome:  filepath.Join(base, "cache"),
		XDGDataHome:   filepath.Join(base, "data"),
		XDGConfigHome: filepath.Join(base, "config"),
	}
	if err := os.Mkdir(paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}

	executor, err := NewExecutor(ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := executor.executablePath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	imageService, err := oci.NewStore(
		paths,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := imageService.Close(); err != nil {
			t.Error(err)
		}
	})
	imageStoreRoot, err := imageService.ImageStoreFile()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	prepared, err := imageService.Prepare(ctx, oci.Request{
		Reference: reference,
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
		if err := prepared.Close(); err != nil {
			t.Error(err)
		}
	})

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
	home, err := storageService.ResolveHome(
		ctx,
		"m2-vertical",
		"default",
		storage.SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := home.Close(); err != nil {
			t.Error(err)
		}
	})
	managedHandles, err := storageService.ResolveManaged(
		ctx,
		storage.ProfileSelection{},
		[]mount.Request{managedRequest},
		nil,
		storage.SeedSource{},
	)
	if err != nil {
		t.Fatal(err)
	}
	managed := managedHandles[0]
	t.Cleanup(func() {
		if err := managed.Close(); err != nil {
			t.Error(err)
		}
	})

	runStorage, err := OpenRunStorage(
		paths.RunStorageDir(),
		RunStorageLimits{},
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
	directories, err := runStorage.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directories.Close(); err != nil {
			t.Error(err)
		}
	})

	project := filepath.Join(base, "projects", "app")
	readOnly := filepath.Join(base, "projects", "read-only")
	for _, directory := range []string{project, readOnly} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtimePath := filepath.Join(base, "runtime", "toby")
	if err := os.MkdirAll(runtimePath, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeRoot, err := os.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}

	rootfs, err := prepared.RootfsFile()
	if err != nil {
		t.Fatal(err)
	}
	upper, err := directories.UpperFile()
	if err != nil {
		t.Fatal(err)
	}
	work, err := directories.WorkFile()
	if err != nil {
		t.Fatal(err)
	}
	homeFile, err := home.File()
	if err != nil {
		t.Fatal(err)
	}
	managedFile, err := managed.File()
	if err != nil {
		t.Fatal(err)
	}
	projectFile, err := os.Open(project)
	if err != nil {
		t.Fatal(err)
	}
	readOnlyFile, err := os.Open(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tobyBinary, err := os.Open(executablePath)
	if err != nil {
		t.Fatal(err)
	}

	binds := []mount.Bind{}
	bindSources := map[string]*os.File{}
	bindParents := map[string]*os.File{}
	if withDNS {
		const resolver = "/etc/resolv.conf"
		resolverFile, err := os.Open(resolver)
		if err != nil {
			t.Fatal(err)
		}
		resolverParent, err := os.Open(filepath.Dir(resolver))
		if err != nil {
			t.Fatal(err)
		}
		binds = append(binds, mount.Bind{
			HostPath: resolver,
			Target:   resolver,
			Access:   mount.AccessReadOnly,
		})
		bindSources[resolver] = resolverFile
		bindParents[resolver] = resolverParent
	}

	sources := Sources{
		ProtectedRoots: ProtectedRoots{
			ImageStore:     imageStoreRoot,
			PersistentData: persistentDataRoot,
			RunStorage:     runStorageRoot,
			Runtime:        runtimeRoot,
		},
		RootFS:       rootfs,
		OverlayUpper: upper,
		OverlayWork:  work,
		Home:         homeFile,
		ManagedDirectories: map[mount.Key]*os.File{
			managed.Entry().Key: managedFile,
		},
		Projects: map[string]*os.File{
			"app":       projectFile,
			"read-only": readOnlyFile,
		},
		Binds:         bindSources,
		BindParents:   bindParents,
		BindNames:     map[string]string{},
		RuntimeAssets: map[string]*os.File{},
		SandboxBinary: tobyBinary,
	}
	for _, bind := range binds {
		sources.BindNames[bind.Target] = filepath.Base(bind.HostPath)
	}
	t.Cleanup(func() {
		for _, file := range sourceFiles(sources) {
			if err := file.Close(); err != nil &&
				!errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		}
	})

	resolved := prepared.Metadata()
	plan, err := BuildPlan(PlanInput{
		RunID: directories.ID(),
		RootFS: RootFS{
			Digest: resolved.Manifest.Digest.String(),
			Path:   prepared.RootfsPath(),
		},
		Overlay: directories.Overlay(),
		Home: Home{
			ID:       home.Identity().ID,
			HostPath: home.HostPath(),
		},
		Projects: []ProjectInput{
			{Name: "app", HostPath: project},
			{Name: "read-only", HostPath: readOnly, ReadOnly: true},
		},
		ManagedDirectories: []mount.Entry{managed.Entry()},
		Binds:              binds,
		SandboxBinaryPath:  executablePath,
		Workdir:            "/",
		Environment:        runtimeEnvironment(prepared.Spec().Runtime.Environment),
		Identity: Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
		CommandArgv:   []string{"/bin/true"},
		ExecutionMode: ExecutionNonInteractive,
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := Render(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	execFD := childExtraFileBaseFD + len(rendered.ExtraFiles)
	statusFD := execFD + 1
	if err := rendered.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := NewRun(plan, sources, directories, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.Close(); err != nil {
			t.Error(err)
		}
	})

	return &verticalFixture{
		run:        run,
		prepared:   prepared,
		home:       home,
		managed:    managed,
		project:    project,
		readOnly:   readOnly,
		execFD:     execFD,
		statusFD:   statusFD,
		executable: executable,
		sources:    sources,
	}
}

func pinCompletedOverlayMount(
	t *testing.T,
	fixture *verticalFixture,
) *os.File {
	t.Helper()

	plan := fixture.run.Plan()
	plan.Command = Command{
		Argv:         []string{"/bin/true"},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}
	invocation, err := Render(plan, fixture.sources)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.Close()

	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer statusReader.Close()
	defer statusWriter.Close()

	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateReader.Close()

	files := append([]*os.File(nil), invocation.ExtraFiles...)
	statusFD := childExtraFileBaseFD + len(files)
	files = append(files, statusWriter)
	gateFD := childExtraFileBaseFD + len(files)
	files = append(files, gateReader)
	args := append(
		[]string{
			"--json-status-fd", strconv.Itoa(statusFD),
			"--block-fd", strconv.Itoa(gateFD),
		},
		invocation.Args...,
	)
	command := exec.Command(fixture.executable, args...)
	command.ExtraFiles = files
	command.Env = []string{}
	var stderr bytes.Buffer
	command.Stderr = &stderr

	monitorStarted := make(chan int, 1)
	trackedResult := trackForegroundStatus(
		statusReader,
		monitorStarted,
		gateWriter,
	)
	if err := command.Start(); err != nil {
		close(monitorStarted)
		t.Fatalf("start pinned overlay mount: %v", err)
	}
	monitorStarted <- command.Process.Pid
	close(monitorStarted)
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}

	waitErr := command.Wait()
	gateReaderErr := gateReader.Close()
	statusWriterErr := statusWriter.Close()
	tracked := <-trackedResult
	if err := errors.Join(
		waitErr,
		gateReaderErr,
		statusWriterErr,
		tracked.statusErr,
		tracked.sandboxErr,
	); err != nil {
		t.Fatalf(
			"complete pinned overlay mount: %v: %s",
			err,
			stderr.String(),
		)
	}
	if !tracked.status.hasExitCode || tracked.status.exitCode != 0 {
		t.Fatalf(
			"pinned overlay status = %#v: %s",
			tracked.status,
			stderr.String(),
		)
	}
	if tracked.sandbox == nil || tracked.sandbox.mntNamespaceFD == nil {
		t.Fatal("pinned overlay mount has no retained mount namespace")
	}
	if err := tracked.sandbox.init.WaitExited(); err != nil {
		t.Fatal(err)
	}

	namespace := tracked.sandbox.mntNamespaceFD
	tracked.sandbox.mntNamespaceFD = nil
	if err := tracked.sandbox.Close(); err != nil {
		t.Fatal(err)
	}

	return namespace
}

func secureVerticalPath(t *testing.T) string {
	t.Helper()

	path, err := os.MkdirTemp(".", ".toby-bwrap-vertical-")
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

func runtimeEnvironment(values []string) []EnvironmentVariable {
	environment := make(map[string]string, len(values))
	for _, value := range values {
		name, content, found := strings.Cut(value, "=")
		if !found || name == "" ||
			name == "HOME" ||
			name == "TOBY_SANDBOX" {
			continue
		}
		environment[name] = content
	}

	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]EnvironmentVariable, 0, len(names))
	for _, name := range names {
		result = append(result, EnvironmentVariable{
			Name:  name,
			Value: environment[name],
		})
	}

	return result
}

func sourceFiles(sources Sources) []*os.File {
	files := []*os.File{
		sources.ProtectedRoots.ImageStore,
		sources.ProtectedRoots.PersistentData,
		sources.ProtectedRoots.RunStorage,
		sources.ProtectedRoots.Runtime,
		sources.RootFS,
		sources.OverlayUpper,
		sources.OverlayWork,
		sources.Home,
		sources.SandboxBinary,
	}
	for _, file := range sources.ManagedDirectories {
		files = append(files, file)
	}
	for _, file := range sources.Projects {
		files = append(files, file)
	}
	for _, file := range sources.Binds {
		files = append(files, file)
	}
	for _, file := range sources.BindParents {
		files = append(files, file)
	}
	for _, file := range sources.RuntimeAssets {
		files = append(files, file)
	}

	return files
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
