package mcpproxy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/container/manager"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/httpproxy"

	dcontainer "github.com/moby/moby/api/types/container"
)

func TestStdioRequestKeepsStdinOpenWithoutTTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	runner := NewDockerRunner(manager.New())
	req := runner.stdioRequest(SidecarSpec{
		Name:      "docs",
		Transport: TransportStdio,
		Command:   []string{"docs-mcp", "--stdio"},
		Env:       map[string]string{"TOKEN": "abc"},
		Image:     "docs:latest",
	})

	if req.Image != "docs:latest" {
		t.Fatalf("image = %q", req.Image)
	}
	if !slices.Equal(req.Cmd, []string{"docs-mcp", "--stdio"}) {
		t.Fatalf("cmd = %#v", req.Cmd)
	}
	if req.Env["TOKEN"] != "abc" || req.Env["TERM"] != "xterm-256color" {
		t.Fatalf("env = %#v", req.Env)
	}
	if req.Started {
		t.Fatal("stdio request must be created unstarted so we can attach before output")
	}
	c := &dcontainer.Config{}
	req.ConfigModifier(c)
	if !c.OpenStdin || c.Tty {
		t.Fatalf("stdio config must keep stdin open without a TTY: %#v", c)
	}
}

func TestHTTPRequestExposesContainerPort(t *testing.T) {
	runner := NewDockerRunner(manager.New())
	req := runner.httpRequest(SidecarSpec{
		Name:      "docs",
		Transport: TransportHTTP,
		Command:   []string{"docs-mcp"},
		Image:     "docs:latest",
		HTTPPort:  3000,
	})

	if !slices.Equal(req.ExposedPorts, []string{"3000/tcp"}) {
		t.Fatalf("exposed ports = %#v", req.ExposedPorts)
	}
	if !req.Started {
		t.Fatal("http request must start so the mapped port can be read")
	}
}

func TestStartProcessNonStdioDiscardsConsoleOutput(t *testing.T) {
	restore, readCaptured := captureConsole(t)
	handle, err := startProcess(context.Background(), []string{"sh", "-c", "printf stdout; printf stderr >&2"}, nil, false, nil)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if result := <-handle.Wait(); result.Err != nil {
		t.Fatalf("process result = %#v", result)
	}
	stdout, stderr := readCaptured()
	if stdout != "" || stderr != "" {
		t.Fatalf("console output = stdout %q, stderr %q", stdout, stderr)
	}
}

func TestStartProcessStdioCapturesStdoutAndDiscardsConsoleStderr(t *testing.T) {
	restore, readCaptured := captureConsole(t)
	handle, err := startProcess(context.Background(), []string{"sh", "-c", "printf protocol; sleep 0.1; printf stderr >&2"}, nil, true, nil)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	protocol := make([]byte, len("protocol"))
	_, err = io.ReadFull(handle.Stdout(), protocol)
	if err != nil {
		t.Fatal(err)
	}
	if string(protocol) != "protocol" {
		t.Fatalf("protocol stdout = %q", protocol)
	}
	if result := <-handle.Wait(); result.Err != nil {
		t.Fatalf("process result = %#v", result)
	}
	stdout, stderr := readCaptured()
	if stdout != "" || stderr != "" {
		t.Fatalf("console output = stdout %q, stderr %q", stdout, stderr)
	}
}

func TestConfigureRegistersRemoteAndLocalProxyURLs(t *testing.T) {
	cfg := testConfig(t, []byte(`
mcps:
  docs:
    type: local
    command: docs-mcp
  remote:
    type: remote
    url: https://example.com/mcp
`))
	proxy := httpproxy.NewService(httpproxy.ServiceParams{})
	svc, err := NewService(ServiceParams{Proxy: proxy, Runtimes: testRuntimes()})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Configure(context.Background(), "127.0.0.1:12345", cfg, Defaults{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docs", "remote"} {
		url, ok := svc.URL(name)
		if !ok || !strings.HasPrefix(url, "http://127.0.0.1:12345/proxy/") {
			t.Fatalf("url %s = %q, %v", name, url, ok)
		}
	}
	status := svc.Status()
	if len(status) != 2 || status[0].Name != "docs" || status[0].Status != StatusRegistered || status[0].Runtime != RuntimeDocker || status[0].Transport != TransportStdio {
		t.Fatalf("status = %#v", status)
	}
}

func TestSidecarSpecImagePrecedence(t *testing.T) {
	cfg := testConfig(t, []byte(`
sandbox:
  mcp:
    runtime:
      docker:
        image: default-mcp:latest
mcps:
  docs:
    type: local
    runtime:
      docker:
        image: docs:latest
    command: docs-mcp
  fallback:
    type: local
    command: fallback-mcp
`))
	servers := cfg.MCPServers()
	defaults := Defaults{Runtime: cfg.MCPSandbox().Runtime, EffectiveImage: "main:latest"}

	docs, err := sidecarSpec("docs", servers["docs"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if docs.Image != "docs:latest" {
		t.Fatalf("docs image = %q", docs.Image)
	}
	fallback, err := sidecarSpec("fallback", servers["fallback"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Image != "default-mcp:latest" {
		t.Fatalf("fallback image = %q", fallback.Image)
	}
}

func testRuntimes() []Runtime {
	return []Runtime{NewDockerRunner(manager.New())}
}

func testConfig(t *testing.T, data []byte) *tobyconfig.Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func captureConsole(t *testing.T) (func(), func() (string, string)) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW
	restored := false
	restore := func() {
		if restored {
			return
		}
		os.Stdout, os.Stderr = oldStdout, oldStderr
		restored = true
	}
	t.Cleanup(func() {
		restore()
		_ = stdoutW.Close()
		_ = stderrW.Close()
		_ = stdoutR.Close()
		_ = stderrR.Close()
	})
	readCaptured := func() (string, string) {
		t.Helper()
		restore()
		_ = stdoutW.Close()
		_ = stderrW.Close()
		stdout, err := io.ReadAll(stdoutR)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := io.ReadAll(stderrR)
		if err != nil {
			t.Fatal(err)
		}
		return string(stdout), string(stderr)
	}
	return restore, readCaptured
}
