package sidecar

// Exercises immutable image planning, final-component mount pinning, private
// network rendering, and process cleanup.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localstdio"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/sandboxgateway"
)

type recordingProgressReporter struct {
	events []protocol.AcquireProgress
}

var _ mcpgateway.ProgressReporter = (*recordingProgressReporter)(nil)

func (r *recordingProgressReporter) Report(
	event protocol.AcquireProgress,
) error {
	r.events = append(r.events, event)
	return nil
}

func TestServiceResolvesImmutableMetadataAndClosesImage(t *testing.T) {
	service, images, _, storage := newTestPreparer(t)
	defer storage.Close()
	progress := &recordingProgressReporter{}

	metadata, err := service.Resolve(t.Context(), Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Network: resource.NetworkHost,
	}, progress)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ImmutableImage !=
		"registry.example/test/mcp@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("immutable image = %q", metadata.ImmutableImage)
	}
	if metadata.RootFSDigest != "sha256:"+strings.Repeat("b", 64) ||
		metadata.Workdir != "/srv" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if image := images.last(); image == nil || !image.closed {
		t.Fatal("metadata resolution retained its OCI lease")
	}
	if images.progressCount() != 1 {
		t.Fatalf(
			"image progress callbacks = %d, want 1",
			images.progressCount(),
		)
	}
}

func TestPreparedStartsWithPinnedMountAndCleansGeneration(t *testing.T) {
	service, images, executor, storage := newTestPreparer(t)
	defer storage.Close()

	base := t.TempDir()
	mountPath := filepath.Join(base, "mount")
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := service.Prepare(t.Context(), Definition{
		Image:       "registry.example/test/mcp:latest",
		Command:     []string{"/bin/mcp", "--socket", layout.Runtime + "/mcp.sock"},
		Environment: map[string]string{"TOKEN": "secret"},
		Mounts: []mcpgateway.Mount{{
			Source: mountPath,
			Target: "/var/lib/mcp",
			Access: mount.AccessRegular,
			Scope:  resource.ScopeHome,
		}},
		Network: resource.NetworkPrivate,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	oldPath := mountPath + ".old"
	if err := os.Rename(mountPath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}

	process, err := prepared.Start(
		t.Context(),
		bwrap.ProcessIO{},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := process.RuntimePath()
	if runtimePath == "" {
		t.Fatal("started HTTP sidecar has no runtime path")
	}
	if info, err := os.Stat(runtimePath); err != nil || !info.IsDir() {
		t.Fatalf("sidecar runtime path = %q: %v", runtimePath, err)
	}

	captured := executor.lastInvocation()
	if !containsSequence(captured.args, "--unshare-net") {
		t.Fatalf("private sidecar args = %q", captured.args)
	}
	bindInfo := captured.bound["/var/lib/mcp"]
	if bindInfo == nil || !os.SameFile(bindInfo, original) {
		t.Fatal("sidecar reopened the replaced mount pathname")
	}
	if captured.bound[layout.Runtime] == nil {
		t.Fatal("sidecar did not bind its private runtime directory")
	}

	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("sidecar process did not finish cleanup")
	}
	if err := process.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar runtime survived cleanup: %v", err)
	}
	if image := images.last(); image == nil || !image.closed {
		t.Fatal("sidecar process retained its OCI lease")
	}
}

func TestProcessCleanupCompletesBoundedRunDirectoryBatches(t *testing.T) {
	service, images, _, storage := newTestPreparerWithLimits(
		t,
		bwrap.RunStorageLimits{MaxCleanupEntries: 3},
	)
	defer storage.Close()

	prepared, err := service.Prepare(t.Context(), Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Network: resource.NetworkPrivate,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	process, err := prepared.Start(t.Context(), bwrap.ProcessIO{}, true)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := process.RuntimePath()
	runRoot := filepath.Dir(runtimePath)
	for index := range 8 {
		name := fmt.Sprintf("entry-%d", index)
		if err := os.WriteFile(
			filepath.Join(runtimePath, name),
			[]byte(name),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("sidecar process did not finish bounded cleanup")
	}
	if err := process.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar run remains after bounded cleanup: %v", err)
	}
	if image := images.last(); image == nil || !image.closed {
		t.Fatal("sidecar process retained its OCI lease")
	}
}

func TestPinnedMountSurvivesPlanningToGenerationStart(t *testing.T) {
	service, _, executor, storage := newTestPreparer(t)
	defer storage.Close()

	base := t.TempDir()
	mountPath := filepath.Join(base, "mount")
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Mounts: []mcpgateway.Mount{{
			Source: mountPath,
			Target: "/var/lib/mcp",
			Access: mount.AccessRegular,
			Scope:  resource.ScopeHome,
		}},
		Network: resource.NetworkHost,
	}

	capabilities, err := service.PinMounts(
		t.Context(),
		definition.Mounts,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer capabilities.Close()
	if _, err := service.Resolve(
		t.Context(),
		definition,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(mountPath, mountPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}

	prepared, err := service.PreparePinned(
		t.Context(),
		definition,
		capabilities,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	process, err := prepared.Start(
		t.Context(),
		bwrap.ProcessIO{},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	captured := executor.lastInvocation()
	bindInfo := captured.bound["/var/lib/mcp"]
	if bindInfo == nil || !os.SameFile(bindInfo, original) {
		t.Fatal(
			"generation startup reopened the post-planning mount pathname",
		)
	}

	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("sidecar process did not finish cleanup")
	}
}

func TestStdioLauncherPinsTargetAcrossConnectorProcesses(t *testing.T) {
	service, images, executor, storage := newTestPreparer(t)
	defer storage.Close()
	launcher, err := NewStdioLauncher(service, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}

	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := launcher.Prepare(
		t.Context(),
		localstdio.Launch{
			Image:   "registry.example/test/mcp:latest",
			Command: []string{"/bin/mcp"},
			Mounts: []mcpgateway.Mount{{
				Source: mountPath,
				Target: "/data",
				Access: mount.AccessReadOnly,
				Scope:  resource.ScopeHome,
			}},
			Network: resource.NetworkHost,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if images.callCount() != 1 || executor.callCount() != 0 {
		t.Fatalf(
			"target preparation = images %d, processes %d; want 1, 0",
			images.callCount(),
			executor.callCount(),
		)
	}

	if err := os.Rename(mountPath, mountPath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}

	server, client := stdioTestSocketPair(t)
	defer server.Close()
	defer client.Close()
	serveCtx, cancelServe := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- prepared.Serve(serveCtx, server)
	}()

	eventuallySidecar(t, time.Second, func() bool {
		return executor.callCount() == 1
	})
	captured := executor.lastInvocation()
	if bound := captured.bound["/data"]; bound == nil ||
		!os.SameFile(bound, original) {
		t.Fatal("stdio connector reopened the replaced mount pathname")
	}
	if images.callCount() != 2 {
		t.Fatalf(
			"image preparations after one connector = %d, want 2",
			images.callCount(),
		)
	}

	cancelServe()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("stdio connector did not reap after cancellation")
	}
}

func TestStdioReleaseTracksProcessPastTerminationGrace(t *testing.T) {
	service, images, executor, storage := newTestPreparer(t)
	defer storage.Close()

	const grace = 10 * time.Millisecond
	executor.holdNextProcess()
	launcher, err := NewStdioLauncher(service, grace, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := localstdio.NewResolver(launcher, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "stdio",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetLocal,
				Transport: mcpgateway.TransportStdio,
				Image:     "registry.example/test/mcp:latest",
				Command:   []string{"/bin/mcp"},
				Scope:     resource.ScopeRun,
				Network:   resource.NetworkHost,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	server, client := stdioTestSocketPair(t)
	defer server.Close()
	defer client.Close()
	connectorDone := make(chan struct{})
	go func() {
		target.Target().ServeConnector(t.Context(), server)
		close(connectorDone)
	}()

	eventuallySidecar(t, time.Second, func() bool {
		return executor.callCount() == 1
	})
	background := executor.lastProcess()
	releaseDone := make(chan error, 1)
	go func() {
		releaseCtx, cancelRelease := context.WithTimeout(
			t.Context(),
			5*grace,
		)
		defer cancelRelease()
		releaseDone <- target.Release(releaseCtx)
	}()

	select {
	case <-background.killed:
	case <-time.After(time.Second):
		t.Fatal("stdio process did not receive its bounded kill attempt")
	}
	select {
	case err := <-releaseDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Release() before process reap error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Release() did not honor its caller deadline")
	}
	select {
	case <-connectorDone:
		t.Fatal("connector ownership ended before the process reaped")
	default:
	}
	select {
	case <-background.Done():
		t.Fatal("test process reaped before explicit completion")
	default:
	}
	if image := images.last(); image == nil || image.closed {
		t.Fatal("stdio process released its image before reap")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(
		t.Context(),
		2*grace,
	)
	err = resolver.Shutdown(shutdownCtx)
	cancelShutdown()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() before process reap error = %v", err)
	}

	background.complete()
	if err := target.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connectorDone:
	default:
		t.Fatal("released target did not join its connector owner")
	}
	if image := images.last(); image == nil || !image.closed {
		t.Fatal("reaped stdio process retained its image")
	}
	if err := resolver.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioPreparationStopsWhenConnectorPeerDisconnects(
	t *testing.T,
) {
	service, images, executor, storage := newTestPreparer(t)
	defer storage.Close()
	launcher, err := NewStdioLauncher(service, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := launcher.Prepare(
		t.Context(),
		localstdio.Launch{
			Image:   "registry.example/test/mcp:latest",
			Command: []string{"/bin/mcp"},
			Network: resource.NetworkHost,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()

	imageStarted := images.blockNext()
	endpointDirectory := t.TempDir()
	if err := os.Chmod(endpointDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	endpoint, err := sandboxgateway.Listen(
		filepath.Join(endpointDirectory, "sandbox.sock"),
		map[string]sandboxgateway.Opener{
			"stdio": sandboxgateway.OpenFunc(func(
				ctx context.Context,
			) (io.ReadWriteCloser, error) {
				gateway, backend := net.Pipe()
				go func() {
					defer close(serveDone)
					defer backend.Close()

					_ = prepared.Serve(ctx, backend)
				}()

				return gateway, nil
			}),
		},
		sandboxgateway.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()

	clientCtx, cancelClient := context.WithCancel(t.Context())
	input, inputWriter := io.Pipe()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- sandboxgateway.Connect(
			clientCtx,
			endpoint.Path(),
			"stdio",
			input,
			io.Discard,
		)
	}()

	select {
	case <-imageStarted:
	case <-time.After(time.Second):
		t.Fatal("connector did not begin OCI preparation")
	}
	cancelClient()
	_ = inputWriter.Close()

	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("disconnected helper did not return")
	}
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("disconnected helper did not stop its prepared backend")
	}
	if executor.callCount() != 0 {
		t.Fatal("disconnected helper started a stdio sidecar")
	}
}

func TestPreparePinnedRejectsMismatchedOrClosedMountCapabilities(
	t *testing.T,
) {
	service, _, _, storage := newTestPreparer(t)
	defer storage.Close()

	source := t.TempDir()
	mounts := []mcpgateway.Mount{{
		Source: source,
		Target: "/data",
		Access: mount.AccessRegular,
		Scope:  resource.ScopeHome,
	}}
	capabilities, err := service.PinMounts(t.Context(), mounts)
	if err != nil {
		t.Fatal(err)
	}

	definition := Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Mounts: append(
			[]mcpgateway.Mount(nil),
			mounts...,
		),
		Network: resource.NetworkHost,
	}
	definition.Mounts[0].Target = "/other"
	if _, err := service.PreparePinned(
		t.Context(),
		definition,
		capabilities,
		nil,
	); err == nil {
		t.Fatal("PreparePinned accepted a mismatched mount definition")
	}

	if err := capabilities.Close(); err != nil {
		t.Fatal(err)
	}
	definition.Mounts[0].Target = "/data"
	if _, err := service.PreparePinned(
		t.Context(),
		definition,
		capabilities,
		nil,
	); err == nil {
		t.Fatal("PreparePinned accepted closed mount capabilities")
	}
}

func TestPrepareRejectsOverlappingMountsBeforeOpeningImage(t *testing.T) {
	service, images, _, storage := newTestPreparer(t)
	defer storage.Close()

	_, err := service.Prepare(t.Context(), Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Mounts: []mcpgateway.Mount{
			{
				Source: "/one",
				Target: "/data",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			},
			{
				Source: "/two",
				Target: "/data/nested",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			},
		},
		Network: resource.NetworkHost,
	}, nil)
	if err == nil {
		t.Fatal("Prepare() accepted overlapping mounts")
	}
	if images.calls != 0 {
		t.Fatal("invalid definition triggered OCI preparation")
	}
}

func TestPinMountsRejectsSymbolicLinkSource(t *testing.T) {
	service, images, _, storage := newTestPreparer(t)
	defer storage.Close()

	target := t.TempDir()
	source := filepath.Join(t.TempDir(), "mount")
	if err := os.Symlink(target, source); err != nil {
		t.Fatal(err)
	}

	capabilities, err := service.PinMounts(
		t.Context(),
		[]mcpgateway.Mount{{
			Source: source,
			Target: "/data",
			Access: mount.AccessRegular,
			Scope:  resource.ScopeHome,
		}},
	)
	if capabilities != nil {
		_ = capabilities.Close()
		t.Fatal("PinMounts returned capabilities for a symbolic link")
	}
	if err == nil || !strings.Contains(
		err.Error(),
		"unsupported filesystem type",
	) {
		t.Fatalf("PinMounts symbolic-link error = %v", err)
	}
	if images.calls != 0 {
		t.Fatal("mount pinning unexpectedly prepared an OCI image")
	}
}

type testImagePreparer struct {
	mu       sync.Mutex
	root     string
	calls    int
	progress int
	prepared []*testImage
	block    chan struct{}
	started  chan struct{}
}

var _ ImagePreparer = (*testImagePreparer)(nil)

func (p *testImagePreparer) PrepareImage(
	ctx context.Context,
	_ string,
	progress mcpgateway.ProgressReporter,
) (Image, error) {
	p.mu.Lock()
	if progress != nil {
		p.progress++
	}
	block := p.block
	started := p.started
	p.block = nil
	p.started = nil
	p.mu.Unlock()
	if block != nil {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		}
	}

	image := &testImage{
		root: p.root,
		resolved: oci.Metadata{
			Reference:  "registry.example/test/mcp:latest",
			Repository: "registry.example/test/mcp",
		},
		spec: oci.Spec{
			Manifest: ocispec.Descriptor{
				Digest: digest.Digest(
					"sha256:" + strings.Repeat("a", 64),
				),
			},
			Config: ocispec.Descriptor{
				Digest: digest.Digest(
					"sha256:" + strings.Repeat("b", 64),
				),
			},
			Runtime: oci.RuntimeConfig{
				Environment: []string{
					"PATH=/usr/bin:/bin",
					"HOME=/root",
				},
				Workdir: "/srv",
			},
		},
	}

	p.mu.Lock()
	p.calls++
	p.prepared = append(p.prepared, image)
	p.mu.Unlock()
	return image, nil
}

func (p *testImagePreparer) progressCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.progress
}

func (p *testImagePreparer) last() *testImage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.prepared) == 0 {
		return nil
	}
	return p.prepared[len(p.prepared)-1]
}

func (p *testImagePreparer) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

func (p *testImagePreparer) blockNext() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.block = make(chan struct{})
	p.started = make(chan struct{})
	return p.started
}

type testImage struct {
	root     string
	resolved oci.Metadata
	spec     oci.Spec
	closed   bool
}

var _ Image = (*testImage)(nil)

func (i *testImage) Metadata() oci.Metadata {
	return i.resolved
}

func (i *testImage) RootfsPath() string {
	if i.closed {
		return ""
	}
	return i.root
}

func (i *testImage) RootfsFile() (*os.File, error) {
	if i.closed {
		return nil, os.ErrInvalid
	}
	return os.Open(i.root)
}

func (i *testImage) Spec() oci.Spec {
	return i.spec
}

func (i *testImage) Close() error {
	i.closed = true
	return nil
}

type testExecutor struct {
	mu          sync.Mutex
	invocations []testInvocation
	processes   []*testBackground
	holdNext    bool
}

var _ BackgroundExecutor = (*testExecutor)(nil)

type testInvocation struct {
	args  []string
	bound map[string]os.FileInfo
}

func (e *testExecutor) StartBackground(
	_ context.Context,
	invocation *bwrap.Invocation,
	_ bwrap.ProcessIO,
) (bwrap.BackgroundProcess, error) {
	args, err := sidecarInvocationArguments(invocation)
	if err != nil {
		_ = invocation.Close()
		return nil, err
	}
	captured := testInvocation{
		args:  args,
		bound: make(map[string]os.FileInfo),
	}
	for index := 0; index+2 < len(args); index++ {
		if args[index] != "--bind-fd" &&
			args[index] != "--ro-bind-fd" {
			continue
		}
		descriptor, err := strconv.Atoi(args[index+1])
		if err != nil || descriptor < 3 {
			continue
		}
		extraIndex := descriptor - 3
		if extraIndex >= len(invocation.ExtraFiles) {
			continue
		}
		info, err := invocation.ExtraFiles[extraIndex].Stat()
		if err != nil {
			invocation.Close()
			return nil, err
		}
		captured.bound[args[index+2]] = info
	}
	if err := invocation.Close(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	hold := e.holdNext
	e.holdNext = false
	process := &testBackground{
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
		killed:  make(chan struct{}),
		hold:    hold,
	}
	e.invocations = append(e.invocations, captured)
	e.processes = append(e.processes, process)
	e.mu.Unlock()
	return process, nil
}

func sidecarInvocationArguments(
	invocation *bwrap.Invocation,
) ([]string, error) {
	if len(invocation.Args) != 2 ||
		invocation.Args[0] != "--args" {
		return append([]string(nil), invocation.Args...), nil
	}

	descriptor, err := strconv.Atoi(invocation.Args[1])
	if err != nil || descriptor < 3 {
		return nil, fmt.Errorf(
			"invalid sidecar argument descriptor %q",
			invocation.Args[1],
		)
	}
	index := descriptor - 3
	if index >= len(invocation.ExtraFiles) ||
		invocation.ExtraFiles[index] == nil {
		return nil, fmt.Errorf(
			"sidecar argument descriptor %d is unavailable",
			descriptor,
		)
	}

	info, err := invocation.ExtraFiles[index].Stat()
	if err != nil {
		return nil, err
	}
	payload := make([]byte, info.Size())
	if _, err := invocation.ExtraFiles[index].ReadAt(
		payload,
		0,
	); err != nil {
		return nil, err
	}
	if len(payload) == 0 ||
		payload[len(payload)-1] != 0 {
		return nil, fmt.Errorf(
			"sidecar argument payload is not NUL terminated",
		)
	}

	parts := bytes.Split(payload[:len(payload)-1], []byte{0})
	args := make([]string, len(parts))
	for index, part := range parts {
		args[index] = string(part)
	}

	return args, nil
}

func (e *testExecutor) lastInvocation() testInvocation {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.invocations[len(e.invocations)-1]
}

func (e *testExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return len(e.invocations)
}

func (e *testExecutor) holdNextProcess() {
	e.mu.Lock()
	e.holdNext = true
	e.mu.Unlock()
}

func (e *testExecutor) lastProcess() *testBackground {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.processes[len(e.processes)-1]
}

type testBackground struct {
	doneOnce sync.Once
	stopOnce sync.Once
	killOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
	killed   chan struct{}
	hold     bool
}

var _ bwrap.BackgroundProcess = (*testBackground)(nil)

func (p *testBackground) Done() <-chan struct{} {
	return p.done
}

func (*testBackground) Err() error {
	return nil
}

func (p *testBackground) Stop(context.Context) error {
	p.stopOnce.Do(func() { close(p.stopped) })
	if !p.hold {
		p.complete()
	}
	return nil
}

func (p *testBackground) Kill(context.Context) error {
	p.killOnce.Do(func() { close(p.killed) })
	if !p.hold {
		p.complete()
	}
	return nil
}

func (p *testBackground) complete() {
	p.doneOnce.Do(func() { close(p.done) })
}

func newTestPreparer(
	t *testing.T,
) (*Preparer, *testImagePreparer, *testExecutor, *bwrap.RunStorage) {
	t.Helper()

	return newTestPreparerWithLimits(t, bwrap.RunStorageLimits{})
}

func newTestPreparerWithLimits(
	t *testing.T,
	limits bwrap.RunStorageLimits,
) (*Preparer, *testImagePreparer, *testExecutor, *bwrap.RunStorage) {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(
		workingDirectory,
		".toby-sidecar-test-",
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
	rootfsPath := filepath.Join(base, "rootfs")
	if err := os.Mkdir(rootfsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := bwrap.OpenRunStorage(
		filepath.Join(base, "runs"),
		limits,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	images := &testImagePreparer{root: rootfsPath}
	executor := &testExecutor{}
	service, err := New(images, storage, executor, nil)
	if err != nil {
		t.Fatal(err)
	}

	return service, images, executor, storage
}

func containsSequence(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func stdioTestSocketPair(
	t *testing.T,
) (*net.UnixConn, *net.UnixConn) {
	t.Helper()

	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{
			Name: filepath.Join(t.TempDir(), "stdio.sock"),
			Net:  "unix",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan *net.UnixConn, 1)
	go func() {
		connection, _ := listener.AcceptUnix()
		accepted <- connection
	}()
	client, err := net.DialUnix(
		"unix",
		nil,
		listener.Addr().(*net.UnixAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	if server == nil {
		client.Close()
		t.Fatal("accept stdio test socket failed")
	}

	return server, client
}

func eventuallySidecar(
	t *testing.T,
	timeout time.Duration,
	condition func() bool,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	if !condition() {
		t.Fatal("condition did not become true")
	}
}
