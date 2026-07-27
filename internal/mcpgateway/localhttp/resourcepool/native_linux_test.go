//go:build linux

package resourcepool

// Exercises native local HTTP planning and startup through the real sidecar
// service while substituting only OCI preparation and Bubblewrap execution.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localhttp"
	"petris.dev/toby/internal/mcpgateway/sidecar"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

const (
	nativeTestManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nativeTestRootFSDigest   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestNativePlansAndStartsPinnedUnixSidecar(t *testing.T) {
	native, images, executor := newNativeTestRuntime(t)

	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	originalMount, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}

	definition := localhttp.Definition{
		Image: "registry.example/test/mcp:latest",
		Command: []string{
			"/bin/mcp",
			"--socket",
			layout.Runtime + "/mcp.sock",
		},
		Environment: map[string]string{
			"TOKEN": "test-secret",
		},
		Endpoint: mcpgateway.Endpoint{
			Kind:   mcpgateway.EndpointUnix,
			Socket: layout.Runtime + "/mcp.sock",
			Path:   "/mcp",
		},
		Mounts: []mcpgateway.Mount{{
			Source: mountPath,
			Target: "/var/lib/mcp",
			Access: mount.AccessRegular,
			Scope:  resource.ScopeHome,
		}},
		Scope:         resource.ScopeHome,
		ScopeIdentity: "home-test",
		Network:       resource.NetworkHost,
		IdleTimeout: mcpgateway.Duration{
			Duration: time.Minute,
		},
	}

	plan, err := native.Plan(t.Context(), definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closePlan(&plan); err != nil {
			t.Error(err)
		}
	})

	immutableImage := "registry.example/test/mcp@" +
		nativeTestManifestDigest
	if plan.Definition.Image != immutableImage {
		t.Fatalf(
			"planned image = %q, want %q",
			plan.Definition.Image,
			immutableImage,
		)
	}
	if plan.Resource.ManifestDigest != nativeTestManifestDigest ||
		plan.Resource.RootFSDigest != nativeTestRootFSDigest {
		t.Fatalf("planned resource = %#v", plan.Resource)
	}
	if got := images.references(); len(got) != 1 ||
		got[0] != definition.Image {
		t.Fatalf("planning image references = %q", got)
	}
	if image := images.image(0); image == nil || !image.isClosed() {
		t.Fatal("planning retained its resolved image")
	}

	replacedPath := mountPath + ".original"
	if err := os.Rename(mountPath, replacedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementMount, err := os.Stat(mountPath)
	if err != nil {
		t.Fatal(err)
	}

	instance, err := native.Start(t.Context(), plan, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cancel()

		_ = instance.Kill(cleanupCtx)
		select {
		case <-instance.Done():
		case <-cleanupCtx.Done():
			t.Error("native local HTTP sidecar cleanup timed out")
		}
	})

	if got := images.references(); len(got) != 2 ||
		got[1] != immutableImage {
		t.Fatalf("startup image references = %q", got)
	}
	runningImage := images.image(1)
	if runningImage == nil || runningImage.isClosed() {
		t.Fatal("running sidecar released its prepared image")
	}

	captured := executor.capture()
	if captured.runtimeTarget != layout.Runtime {
		t.Fatalf(
			"sidecar runtime target = %q, want %q",
			captured.runtimeTarget,
			layout.Runtime,
		)
	}
	if captured.runtimePath == "" ||
		filepath.Dir(captured.socketPath) != captured.runtimePath {
		t.Fatalf(
			"sidecar runtime path = %q, socket = %q",
			captured.runtimePath,
			captured.socketPath,
		)
	}
	if info, err := os.Lstat(captured.socketPath); err != nil ||
		info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("native sidecar socket = %q: %v", captured.socketPath, err)
	}

	boundMount := captured.binds["/var/lib/mcp"]
	if boundMount == nil || !os.SameFile(boundMount, originalMount) {
		t.Fatal("sidecar startup reopened the replaced mount pathname")
	}
	if os.SameFile(boundMount, replacementMount) {
		t.Fatal("sidecar startup used the replacement mount inode")
	}

	upstream, err := instance.Upstream()
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Endpoint != "http://mcp.local/mcp" ||
		upstream.HTTPClient == nil {
		t.Fatalf("native upstream = %#v", upstream)
	}
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		upstream.Endpoint,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := upstream.HTTPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal(errors.Join(readErr, closeErr))
	}
	if response.StatusCode != http.StatusOK ||
		string(body) != `{"ok":true}` {
		t.Fatalf(
			"native upstream response = %d %q",
			response.StatusCode,
			body,
		)
	}

	select {
	case received := <-executor.requests:
		if received.host != "mcp.local" ||
			received.path != definition.Endpoint.Path {
			t.Fatalf("native upstream request = %#v", received)
		}
	case <-time.After(time.Second):
		t.Fatal("native sidecar did not receive the HTTP request")
	}

	runtimePath := captured.runtimePath
	if err := instance.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-instance.Done():
	case <-time.After(time.Second):
		t.Fatal("native local HTTP sidecar did not finish cleanup")
	}
	if err := instance.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar runtime survived reap: %v", err)
	}
	if !runningImage.isClosed() {
		t.Fatal("reaped sidecar retained its prepared image")
	}
}

func TestNativePoolSeparatesReplacedMountPath(t *testing.T) {
	native, _, _ := newNativeTestRuntime(t)
	builder, err := resource.NewBuilder(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := New(
		builder,
		native,
		native,
		resource.Options{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := localhttp.Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Endpoint: mcpgateway.Endpoint{
			Kind:   mcpgateway.EndpointUnix,
			Socket: layout.Runtime + "/mcp.sock",
			Path:   "/mcp",
		},
		Mounts: []mcpgateway.Mount{{
			Source: mountPath,
			Target: "/data",
			Access: mount.AccessReadOnly,
			Scope:  resource.ScopeHome,
		}},
		Scope:         resource.ScopeHome,
		ScopeIdentity: "home-test",
		Network:       resource.NetworkHost,
	}

	firstValue, err := pool.Prepare(t.Context(), definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := firstValue.(*prepared)
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})

	if err := os.Rename(mountPath, mountPath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mountPath, 0o700); err != nil {
		t.Fatal(err)
	}

	secondValue, err := pool.Prepare(t.Context(), definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := secondValue.(*prepared)
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})

	if first.key == second.key {
		t.Fatal(
			"replaced mount pathname aliased the retained source inode",
		)
	}
}

func TestNativeRejectsSocketWithoutMCPProtocolReadiness(t *testing.T) {
	native, _, executor := newNativeTestRuntime(t)
	executor.invalidMCP = true

	plan, err := native.Plan(t.Context(), localhttp.Definition{
		Image:   "registry.example/test/mcp:latest",
		Command: []string{"/bin/mcp"},
		Endpoint: mcpgateway.Endpoint{
			Kind:   mcpgateway.EndpointUnix,
			Socket: layout.Runtime + "/mcp.sock",
			Path:   "/mcp",
		},
		Scope:   resource.ScopeUser,
		Network: resource.NetworkHost,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closePlan(&plan); err != nil {
			t.Error(err)
		}
	})

	readyCtx, cancel := context.WithTimeout(
		t.Context(),
		2*time.Second,
	)
	defer cancel()

	started := time.Now()
	instance, err := native.Start(readyCtx, plan, 1)
	if instance == nil {
		t.Fatal("protocol readiness failure omitted the started instance")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cleanupCancel()
		_ = instance.Kill(cleanupCtx)
		select {
		case <-instance.Done():
		case <-cleanupCtx.Done():
			t.Error("invalid MCP sidecar cleanup timed out")
		}
	})
	if err == nil ||
		!strings.Contains(err.Error(), "initialize local HTTP MCP endpoint") {
		t.Fatalf("protocol readiness error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf(
			"deterministic protocol failure retried for %s",
			elapsed,
		)
	}
}

type nativeTestImages struct {
	mu       sync.Mutex
	root     string
	refs     []string
	prepared []*nativeTestImage
}

var _ sidecar.ImagePreparer = (*nativeTestImages)(nil)

func (p *nativeTestImages) PrepareImage(
	ctx context.Context,
	value string,
	_ mcpgateway.ProgressReporter,
) (sidecar.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prepared := &nativeTestImage{
		root: p.root,
		resolved: oci.Metadata{
			Reference:  value,
			Repository: "registry.example/test/mcp",
		},
		spec: oci.Spec{
			Manifest: ocispec.Descriptor{
				Digest: digest.Digest(nativeTestManifestDigest),
			},
			Config: ocispec.Descriptor{
				Digest: digest.Digest(nativeTestRootFSDigest),
			},
			Runtime: oci.RuntimeConfig{
				Environment: []string{"PATH=/usr/bin:/bin"},
				Workdir:     "/srv",
			},
		},
	}

	p.mu.Lock()
	p.refs = append(p.refs, value)
	p.prepared = append(p.prepared, prepared)
	p.mu.Unlock()

	return prepared, nil
}

func (p *nativeTestImages) references() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.refs...)
}

func (p *nativeTestImages) image(index int) *nativeTestImage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.prepared) {
		return nil
	}

	return p.prepared[index]
}

type nativeTestImage struct {
	mu       sync.Mutex
	root     string
	resolved oci.Metadata
	spec     oci.Spec
	closed   bool
}

var _ sidecar.Image = (*nativeTestImage)(nil)

func (i *nativeTestImage) Metadata() oci.Metadata {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.resolved
}

func (i *nativeTestImage) RootfsPath() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return ""
	}

	return i.root
}

func (i *nativeTestImage) RootfsFile() (*os.File, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, os.ErrInvalid
	}

	return os.Open(i.root)
}

func (i *nativeTestImage) Spec() oci.Spec {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.spec
}

func (i *nativeTestImage) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.closed = true
	return nil
}

func (i *nativeTestImage) isClosed() bool {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.closed
}

type nativeTestExecutor struct {
	mu         sync.Mutex
	socketName string
	requests   chan nativeTestRequest
	invalidMCP bool
	captured   nativeTestInvocation
}

var _ sidecar.BackgroundExecutor = (*nativeTestExecutor)(nil)

type nativeTestInvocation struct {
	runtimeTarget string
	runtimePath   string
	socketPath    string
	binds         map[string]os.FileInfo
}

type nativeTestRequest struct {
	host string
	path string
}

func (e *nativeTestExecutor) StartBackground(
	ctx context.Context,
	invocation *bwrap.Invocation,
	_ bwrap.ProcessIO,
) (bwrap.BackgroundProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, invocation.Close())
	}

	args, err := nativeTestInvocationArguments(invocation)
	if err != nil {
		return nil, errors.Join(err, invocation.Close())
	}
	captured := nativeTestInvocation{
		binds: make(map[string]os.FileInfo),
	}
	for index := 0; index+2 < len(args); index++ {
		if args[index] != "--bind-fd" &&
			args[index] != "--ro-bind-fd" {
			continue
		}

		descriptor, err := strconv.Atoi(args[index+1])
		if err != nil || descriptor < 3 {
			return nil, errors.Join(
				fmt.Errorf(
					"invalid sidecar bind descriptor %q",
					args[index+1],
				),
				invocation.Close(),
			)
		}
		extraIndex := descriptor - 3
		if extraIndex >= len(invocation.ExtraFiles) ||
			invocation.ExtraFiles[extraIndex] == nil {
			return nil, errors.Join(
				fmt.Errorf(
					"sidecar bind descriptor %d is unavailable",
					descriptor,
				),
				invocation.Close(),
			)
		}

		source := invocation.ExtraFiles[extraIndex]
		info, err := source.Stat()
		if err != nil {
			return nil, errors.Join(err, invocation.Close())
		}
		target := args[index+2]
		captured.binds[target] = info
		if target == layout.Runtime {
			captured.runtimeTarget = target
			captured.runtimePath, err = os.Readlink(
				"/proc/self/fd/" +
					strconv.FormatUint(uint64(source.Fd()), 10),
			)
			if err != nil {
				return nil, errors.Join(err, invocation.Close())
			}
		}
	}
	if captured.runtimePath == "" {
		return nil, errors.Join(
			fmt.Errorf("sidecar invocation has no runtime bind"),
			invocation.Close(),
		)
	}

	captured.socketPath = filepath.Join(
		captured.runtimePath,
		e.socketName,
	)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{
			Name: captured.socketPath,
			Net:  "unix",
		},
	)
	if err != nil {
		return nil, errors.Join(err, invocation.Close())
	}
	listener.SetUnlinkOnClose(true)

	server := &http.Server{
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			select {
			case e.requests <- nativeTestRequest{
				host: request.Host,
				path: request.URL.Path,
			}:
			default:
			}

			if e.invalidMCP {
				http.NotFound(writer, request)
				return
			}
			switch request.Method {
			case http.MethodGet:
				writer.WriteHeader(http.StatusMethodNotAllowed)
				return
			case http.MethodDelete:
				writer.WriteHeader(http.StatusNoContent)
				return
			}

			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
				http.Error(
					writer,
					"invalid JSON-RPC request",
					http.StatusBadRequest,
				)
				return
			}
			if message.Method == "initialize" {
				writer.Header().Set(
					"Content-Type",
					"application/json",
				)
				_, _ = fmt.Fprintf(
					writer,
					`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"native-test","version":"1"}}}`,
					message.ID,
				)
				return
			}
			if message.Method == "notifications/initialized" {
				writer.WriteHeader(http.StatusAccepted)
				return
			}

			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, `{"ok":true}`)
		}),
	}
	if err := invocation.Close(); err != nil {
		return nil, errors.Join(err, listener.Close())
	}

	process := newNativeTestBackground(server, listener)
	e.mu.Lock()
	e.captured = captured
	e.mu.Unlock()
	process.start()

	return process, nil
}

func (e *nativeTestExecutor) capture() nativeTestInvocation {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := e.captured
	result.binds = make(map[string]os.FileInfo, len(e.captured.binds))
	for target, info := range e.captured.binds {
		result.binds[target] = info
	}

	return result
}

type nativeTestBackground struct {
	server   *http.Server
	listener *net.UnixListener
	done     chan struct{}

	mu  sync.Mutex
	err error
}

var _ bwrap.BackgroundProcess = (*nativeTestBackground)(nil)

func newNativeTestBackground(
	server *http.Server,
	listener *net.UnixListener,
) *nativeTestBackground {
	return &nativeTestBackground{
		server:   server,
		listener: listener,
		done:     make(chan struct{}),
	}
}

func (p *nativeTestBackground) start() {
	go func() {
		err := p.server.Serve(p.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()
}

func (p *nativeTestBackground) Done() <-chan struct{} {
	return p.done
}

func (p *nativeTestBackground) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.err
}

func (p *nativeTestBackground) Stop(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("stop native test sidecar context is nil")
	}

	return p.server.Shutdown(ctx)
}

func (p *nativeTestBackground) Kill(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("kill native test sidecar context is nil")
	}

	return p.server.Close()
}

func nativeTestInvocationArguments(
	invocation *bwrap.Invocation,
) ([]string, error) {
	if invocation == nil {
		return nil, fmt.Errorf("sidecar invocation is nil")
	}
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
	if info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf(
			"sidecar argument payload size %d is invalid",
			info.Size(),
		)
	}
	payload := make([]byte, info.Size())
	read, err := invocation.ExtraFiles[index].ReadAt(payload, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if read != len(payload) ||
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

func newNativeTestRuntime(
	t *testing.T,
) (*Native, *nativeTestImages, *nativeTestExecutor) {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Clean(filepath.Join(
		workingDirectory,
		"..",
		"..",
		"..",
		"..",
	))
	baseParent := projectRoot
	for {
		parent := filepath.Dir(baseParent)
		if parent == baseParent {
			break
		}
		info, err := os.Stat(parent)
		if err != nil {
			break
		}
		status, ok := info.Sys().(*syscall.Stat_t)
		if !ok ||
			!info.IsDir() ||
			int(status.Uid) != os.Geteuid() ||
			info.Mode().Perm()&0o300 != 0o300 ||
			info.Mode().Perm()&0o022 != 0 {
			break
		}
		baseParent = parent
	}
	base, err := os.MkdirTemp(
		baseParent,
		".toby-http-",
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
		bwrap.RunStorageLimits{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Error(err)
		}
	})

	images := &nativeTestImages{root: rootfsPath}
	executor := &nativeTestExecutor{
		socketName: "mcp.sock",
		requests:   make(chan nativeTestRequest, 4),
	}
	sidecars, err := sidecar.New(images, storage, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	native, err := NewNative(sidecars, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}

	return native, images, executor
}
