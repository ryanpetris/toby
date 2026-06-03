package mcpproxy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control/httpproxy"
)

func TestDockerBuildCommandUsesContainerDefaults(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	runner := &DockerRunner{docker: "docker"}
	spec := SidecarSpec{
		Name:          "docs",
		Runtime:       RuntimeDocker,
		Transport:     TransportStdio,
		Command:       []string{"docs-mcp", "--stdio"},
		Env:           map[string]string{"TOKEN": "abc"},
		DockerImage:   "docs:latest",
		ContainerName: "toby-mcp-docs",
	}

	cmd := runner.BuildCommand(spec)
	assertSequence(t, cmd, []string{"docker", "run", "--rm", "-i"})
	assertSequence(t, cmd, []string{"--name", "toby-mcp-docs"})
	assertSequence(t, cmd, []string{"--env", "TOKEN=abc"})
	assertSequence(t, cmd, []string{"--env", "TERM=xterm-256color"})
	assertSequence(t, cmd, []string{"docs:latest", "docs-mcp", "--stdio"})
	for _, forbidden := range []string{"--mount", "--tmpfs", "--workdir", "--group-add", "--user"} {
		if slices.Contains(cmd, forbidden) {
			t.Fatalf("docker MCP command must not include %s: %#v", forbidden, cmd)
		}
	}
	for _, value := range cmd {
		if strings.HasPrefix(value, "HOME=") {
			t.Fatalf("docker MCP command must not set HOME: %#v", cmd)
		}
	}
}

func TestDockerHTTPBuildCommandPublishesLoopbackPort(t *testing.T) {
	runner := &DockerRunner{docker: "docker"}
	cmd := runner.BuildCommand(SidecarSpec{Runtime: RuntimeDocker, Transport: TransportHTTP, Command: []string{"docs-mcp"}, DockerImage: "docs:latest", HTTPPort: 3000, HostPort: 41000})

	assertSequence(t, cmd, []string{"--publish", "127.0.0.1:41000:3000"})
	if slices.Contains(cmd, "-i") {
		t.Fatalf("http MCP command should not keep stdin open: %#v", cmd)
	}
}

func TestDockerDebugBuildCommandOmitsRemove(t *testing.T) {
	runner := &DockerRunner{docker: "docker"}
	cmd := runner.BuildCommand(SidecarSpec{Runtime: RuntimeDocker, Transport: TransportHTTP, Command: []string{"docs-mcp"}, DockerImage: "docs:latest", ContainerName: "toby-mcp-docs", Debug: true})

	if slices.Contains(cmd, "--rm") {
		t.Fatalf("debug MCP command should not remove container: %#v", cmd)
	}
	assertSequence(t, cmd, []string{"--name", "toby-mcp-docs"})
}

func TestBubblewrapBuildCommandUsesTmpfsHomeWithoutProjectMounts(t *testing.T) {
	runner := &BubblewrapRunner{bwrap: "bwrap"}
	cmd := runner.BuildCommand(SidecarSpec{Runtime: RuntimeBubblewrap, Transport: TransportStdio, Command: []string{"docs-mcp"}, Env: map[string]string{"TOKEN": "abc"}, Home: sidecarHome, Workdir: sidecarHome})

	assertSequence(t, cmd, []string{"bwrap", "--die-with-parent", "--unshare-pid"})
	assertSequence(t, cmd, []string{"--tmpfs", sidecarHome})
	assertSequence(t, cmd, []string{"--setenv", "HOME", sidecarHome})
	assertSequence(t, cmd, []string{"--setenv", "TOKEN", "abc"})
	assertSequence(t, cmd, []string{"--chdir", sidecarHome, "docs-mcp"})
	for _, value := range cmd {
		if strings.Contains(value, "/workspace") || strings.Contains(value, "/context") {
			t.Fatalf("bubblewrap MCP command must not include project/context mounts: %#v", cmd)
		}
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

func TestSidecarSpecDockerImagePrecedence(t *testing.T) {
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
	defaults := Defaults{Runtime: cfg.MCPSandbox().Runtime, EffectiveDockerImage: "main:latest"}

	docs, err := sidecarSpec("docs", servers["docs"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if docs.DockerImage != "docs:latest" {
		t.Fatalf("docs image = %q", docs.DockerImage)
	}
	fallback, err := sidecarSpec("fallback", servers["fallback"], defaults)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.DockerImage != "default-mcp:latest" {
		t.Fatalf("fallback image = %q", fallback.DockerImage)
	}
}

func testRuntimes() []Runtime {
	return []Runtime{&DockerRunner{docker: "docker"}, &BubblewrapRunner{bwrap: "bwrap"}}
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

func assertSequence(t *testing.T, values, sequence []string) {
	t.Helper()
	for i := 0; i+len(sequence) <= len(values); i++ {
		if slices.Equal(values[i:i+len(sequence)], sequence) {
			return
		}
	}
	t.Fatalf("sequence %#v not found in %#v", sequence, values)
}
