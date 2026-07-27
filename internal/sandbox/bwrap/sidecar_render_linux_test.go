//go:build linux

package bwrap

// Exercises fixed sidecar namespace policy, descriptor coverage, and reserved
// target protection.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestRenderSidecarUsesPrivateNetworkAndExactSources(t *testing.T) {
	plan := validSidecarPlan()
	plan.Environment[0].Value = "environment-secret-sentinel"
	plan.Command = append(plan.Command, "command-secret-sentinel")
	sources := validSidecarSources(t, plan)

	invocation, err := RenderSidecar(plan, sources)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.Close()

	if got, want := invocation.Args, []string{"--args", "8"}; !slices.Equal(got, want) {
		t.Fatalf("sidecar public args = %q, want %q", got, want)
	}
	public := strings.Join(invocation.Args, "\x00")
	for _, secret := range []string{
		"environment-secret-sentinel",
		"command-secret-sentinel",
	} {
		if strings.Contains(public, secret) {
			t.Fatalf("sidecar public args expose %q: %q", secret, invocation.Args)
		}
	}

	args, err := invocationArguments(invocation)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"--unshare-net",
		"--cap-drop\x00ALL",
		"--dir\x00" + layout.Runtime +
			"\x00--chmod\x000700\x00" + layout.Runtime,
		"--setenv\x00HOME\x00/tmp",
		"--setenv\x00TOKEN\x00environment-secret-sentinel",
		"--bind-fd",
		"\x00" + layout.Runtime,
		"--chdir\x00/",
		"--\x00/bin/mcp-server",
		"\x00command-secret-sentinel",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sidecar payload args omit %q: %q", want, args)
		}
	}
	if strings.Contains(joined, layout.SandboxBinary()) ||
		strings.Contains(joined, layout.Home) {
		t.Fatalf(
			"sidecar unexpectedly mounts a sandbox helper or private home: %q",
			args,
		)
	}
	if got, want := len(invocation.ExtraFiles), 6; got != want {
		t.Fatalf("sidecar descriptor count = %d, want %d", got, want)
	}
}

func TestExecutorKeepsSidecarSecretsOutOfObservableArgv(t *testing.T) {
	const (
		environmentSecret = "observable-environment-secret-sentinel"
		commandSecret     = "observable-command-secret-sentinel"
	)

	plan := validSidecarPlan()
	plan.Environment[0].Value = environmentSecret
	plan.Command = append(plan.Command, commandSecret)
	invocation, err := RenderSidecar(plan, validSidecarSources(t, plan))
	if err != nil {
		t.Fatal(err)
	}

	fake, err := filepath.Abs("testdata/fake-bwrap-confidential.sh")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		ExecutorOptions{Executable: fake},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Error(err)
		}
	})

	var output bytes.Buffer
	code, err := executor.Execute(t.Context(), invocation, ProcessIO{
		Stdout: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	var observable []string
	var payload []string
	for line := range strings.Lines(output.String()) {
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "argv:"):
			observable = append(
				observable,
				strings.TrimPrefix(line, "argv:"),
			)
		case strings.HasPrefix(line, "payload:"):
			payload = append(
				payload,
				strings.TrimPrefix(line, "payload:"),
			)
		default:
			t.Fatalf("unexpected fake Bubblewrap output %q", line)
		}
	}

	observed := strings.Join(observable, "\x00")
	for _, secret := range []string{environmentSecret, commandSecret} {
		if strings.Contains(observed, secret) {
			t.Fatalf(
				"observable child argv exposes %q: %q",
				secret,
				observable,
			)
		}
	}
	received := strings.Join(payload, "\x00")
	for _, secret := range []string{environmentSecret, commandSecret} {
		if !strings.Contains(received, secret) {
			t.Fatalf(
				"confidential argument payload omits %q: %q",
				secret,
				payload,
			)
		}
	}
}

func TestStartBackgroundRunsConfidentialSidecarArguments(t *testing.T) {
	output := filepath.Join(t.TempDir(), "sidecar-output")
	plan := validSidecarPlan()
	plan.Command = []string{
		"/bin/sh",
		"-c",
		`printf '%s' "$1" >"$2"; /bin/sleep 0.1`,
		"toby-confidential-background",
		"background-secret-sentinel",
		output,
	}
	invocation, err := RenderSidecar(
		plan,
		validSidecarSources(t, plan),
	)
	if err != nil {
		t.Fatal(err)
	}

	process, err := backgroundTestExecutor(t).StartBackground(
		t.Context(),
		invocation,
		ProcessIO{},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForBackgroundDone(t, process)
	if err := process.Err(); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "background-secret-sentinel"; got != want {
		t.Fatalf("background sidecar output = %q, want %q", got, want)
	}
}

func TestRenderSidecarRejectsIncompleteOrAliasedSources(t *testing.T) {
	plan := validSidecarPlan()

	t.Run("missing bind", func(t *testing.T) {
		sources := validSidecarSources(t, plan)
		delete(sources.Binds, "/var/lib/mcp")
		if _, err := RenderSidecar(plan, sources); err == nil {
			t.Fatal("RenderSidecar() accepted a missing bind source")
		}
	})

	t.Run("aliased overlay", func(t *testing.T) {
		sources := validSidecarSources(t, plan)
		sources.OverlayWork = sources.OverlayUpper
		if _, err := RenderSidecar(plan, sources); err == nil {
			t.Fatal("RenderSidecar() accepted aliased overlay sources")
		}
	})
}

func TestSidecarPlanRejectsReservedAndOverlappingMounts(t *testing.T) {
	plan := validSidecarPlan()

	for _, target := range []string{"/", "/proc/data", "/dev", "/tmp/x", "/run/x"} {
		target := target
		t.Run(target, func(t *testing.T) {
			invalid := plan.Clone()
			invalid.Binds[0].Target = target
			if err := invalid.Validate(); err == nil {
				t.Fatalf("Validate() accepted reserved target %q", target)
			}
		})
	}

	invalid := plan.Clone()
	invalid.Binds = append(invalid.Binds, mount.Bind{
		HostPath: "/host/other",
		Target:   "/var/lib/mcp/child",
		Access:   mount.AccessReadOnly,
	})
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() accepted overlapping bind targets")
	}
}

func validSidecarPlan() SidecarPlan {
	return SidecarPlan{
		ID: "run-0123456789abcdef0123456789abcdef",
		RootFS: RootFS{
			Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Path:   "/store/rootfs",
		},
		Overlay: Overlay{
			RunStorageDir: "/state/runs",
			Upper:         "/state/runs/run-0123456789abcdef0123456789abcdef/upper",
			Work:          "/state/runs/run-0123456789abcdef0123456789abcdef/work",
		},
		Binds: []mount.Bind{{
			HostPath: "/host/state",
			Target:   "/var/lib/mcp",
			Access:   mount.AccessRegular,
		}},
		Runtime: &RuntimeAsset{
			HostPath: "/state/runs/run-0123456789abcdef0123456789abcdef/runtime",
			Target:   layout.Runtime,
			Access:   mount.AccessRegular,
		},
		Workdir: "/",
		Environment: []EnvironmentVariable{
			{Name: "TOKEN", Value: "secret"},
		},
		Identity: Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
		Network: NetworkPrivate,
		Command: []string{"/bin/mcp-server"},
	}
}

func validSidecarSources(
	t *testing.T,
	plan SidecarPlan,
) SidecarSources {
	t.Helper()

	sources := SidecarSources{
		RootFS:       openDirectorySource(t),
		OverlayUpper: openDirectorySource(t),
		OverlayWork:  openDirectorySource(t),
		Binds:        make(map[string]*os.File, len(plan.Binds)),
	}
	for _, bind := range plan.Binds {
		sources.Binds[bind.Target] = openDirectorySource(t)
	}
	if plan.Runtime != nil {
		sources.Runtime = openDirectorySource(t)
	}

	return sources
}
