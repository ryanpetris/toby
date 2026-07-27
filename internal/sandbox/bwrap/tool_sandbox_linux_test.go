//go:build linux

package bwrap

// Verifies tool declaration freezing and shared-run lifecycle execution.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"petris.dev/toby/internal/diagnostic/exitcode"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestToolSandboxCollectsNativeDeclarations(t *testing.T) {
	project := validPlan().Projects[0]
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects:       []Project{project},
		ForegroundMode: ExecutionManagedPTY,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	}
	if err := sandbox.AddMount(request); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AddMount(request); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SetEnvironment(t.Context(), "PATH", "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}

	wantRequests := []mount.Request{{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "/toby/home/.local/share/opencode",
		Access: mount.AccessRegular,
	}}
	if got := sandbox.MountRequests(); !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("mount requests = %#v, want %#v", got, wantRequests)
	}
	if got := sandbox.EnvironmentVariables(); !reflect.DeepEqual(
		got,
		[]EnvironmentVariable{{Name: "PATH", Value: "/usr/bin:/bin"}},
	) {
		t.Fatalf("environment = %#v", got)
	}
	if got, found := sandbox.ProjectPath("app"); !found || got != project.Target {
		t.Fatalf("project path = %q, %v", got, found)
	}
}

func TestToolSandboxMergesImageEnvironmentBeforeToolOverrides(t *testing.T) {
	project := validPlan().Projects[0]
	imageEnvironment := []string{
		"PATH=/usr/local/bin:/usr/bin",
		"LANG=C",
		"DUPLICATE=first",
		"DUPLICATE=last",
		"HOME=/root",
	}
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects:         []Project{project},
		ImageEnvironment: imageEnvironment,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := sandbox.PrependEnvironment(
		t.Context(),
		"PATH",
		"/toby/home/.local/bin",
		":",
	); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SetEnvironment(t.Context(), "LANG", "en_US.UTF-8"); err != nil {
		t.Fatal(err)
	}

	want := []EnvironmentVariable{
		{Name: "DUPLICATE", Value: "last"},
		{Name: "LANG", Value: "en_US.UTF-8"},
		{Name: "PATH", Value: "/toby/home/.local/bin:/usr/local/bin:/usr/bin"},
	}
	if got := sandbox.EnvironmentVariables(); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged environment = %#v, want %#v", got, want)
	}
	if err := sandbox.ValidateImageEnvironment(imageEnvironment); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.ValidateImageEnvironment(
		[]string{"PATH=/different"},
	); err == nil {
		t.Fatal("mismatched prepared image environment was accepted")
	}
}

func TestToolSandboxOverridesImageTerminalForInteractiveLaunch(t *testing.T) {
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		ImageEnvironment: []string{"TERM=image-terminal"},
		TerminalType:     "host-terminal",
		ForegroundMode:   ExecutionDirectTerminal,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, found := sandbox.Environment("TERM"); !found ||
		got != "host-terminal" {
		t.Fatalf("TERM = %q, %v, want host-terminal, true", got, found)
	}
}

func TestToolSandboxKeepsImageTerminalForNoninteractiveLaunch(t *testing.T) {
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		ImageEnvironment: []string{"TERM=image-terminal"},
		TerminalType:     "host-terminal",
		ForegroundMode:   ExecutionNonInteractive,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, found := sandbox.Environment("TERM"); !found ||
		got != "image-terminal" {
		t.Fatalf("TERM = %q, %v, want image-terminal, true", got, found)
	}
}

func TestToolSandboxRunsLifecycleAndForegroundThroughAttachedRun(
	t *testing.T,
) {
	run, plan, _, executor := testRun(t)
	var lifecycleOutput bytes.Buffer
	var foregroundOutput bytes.Buffer
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects: plan.Projects,
		StartLifecycleOperation: func(
			context.Context,
			[]string,
			sandboxapi.ExecOptions,
		) (ProcessIO, func(error)) {
			return ProcessIO{Stdout: &lifecycleOutput}, nil
		},
		ForegroundStreams: ProcessIO{
			Stdout: &foregroundOutput,
		},
		ForegroundMode: ExecutionManagedPTY,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SetEnvironment(
		t.Context(),
		"PATH",
		"/usr/bin:/bin",
	); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatal(err)
	}

	if _, err := sandbox.Exec(
		t.Context(),
		[]string{"/bin/true"},
		sandboxapi.ExecOptions{Root: true},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Exec(
		t.Context(),
		[]string{"/bin/true"},
		sandboxapi.ExecOptions{Foreground: true},
	); err != nil {
		t.Fatal(err)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()
	if got, want := executor.modes, []ExecutionMode{
		ExecutionNonInteractive,
		ExecutionManagedPTY,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("execution modes = %q, want %q", got, want)
	}
	if got := executor.streams[0].Stdout; got != &lifecycleOutput {
		t.Fatalf("lifecycle stdout = %T %p", got, got)
	}
	if got := executor.streams[1].Stdout; got != &foregroundOutput {
		t.Fatalf("foreground stdout = %T %p", got, got)
	}
}

func TestToolSandboxControlsHiddenLifecycleOutputWithoutChangingForeground(
	t *testing.T,
) {
	for _, reveal := range []bool{false, true} {
		t.Run(fmt.Sprintf("reveal-%t", reveal), func(t *testing.T) {
			run, plan, _, executor := testRun(t)
			var lifecycleOutput bytes.Buffer
			var foregroundOutput bytes.Buffer
			sandbox, err := NewToolSandbox(ToolSandboxOptions{
				Projects: plan.Projects,
				StartLifecycleOperation: func(
					context.Context,
					[]string,
					sandboxapi.ExecOptions,
				) (ProcessIO, func(error)) {
					return ProcessIO{Stdout: &lifecycleOutput}, nil
				},
				ForegroundStreams: ProcessIO{
					Stdout: &foregroundOutput,
				},
				ForegroundMode:     ExecutionManagedPTY,
				RevealHiddenOutput: reveal,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := sandbox.AddMount(mount.Request{
				Key: mount.Key{
					Type:    mount.TypeTool,
					Name:    "opencode",
					Purpose: "state",
				},
				Target: "~/.local/share/opencode",
			}); err != nil {
				t.Fatal(err)
			}
			if err := sandbox.SetEnvironment(
				t.Context(),
				"PATH",
				"/usr/bin:/bin",
			); err != nil {
				t.Fatal(err)
			}
			if err := sandbox.Attach(run); err != nil {
				t.Fatal(err)
			}

			if _, err := sandbox.Exec(
				t.Context(),
				[]string{"/bin/true"},
				sandboxapi.ExecOptions{HideOutput: true},
			); err != nil {
				t.Fatal(err)
			}
			if _, err := sandbox.Exec(
				t.Context(),
				[]string{"/bin/true"},
				sandboxapi.ExecOptions{Foreground: true},
			); err != nil {
				t.Fatal(err)
			}

			executor.mu.Lock()
			defer executor.mu.Unlock()
			wantLifecycle := io.Writer(io.Discard)
			if reveal {
				wantLifecycle = &lifecycleOutput
			}
			if got := executor.streams[0].Stdout; got != wantLifecycle {
				t.Fatalf(
					"hidden lifecycle stdout = %T, want %T",
					got,
					wantLifecycle,
				)
			}
			if got := executor.streams[1].Stdout; got != &foregroundOutput {
				t.Fatalf(
					"foreground stdout = %T %p",
					got,
					got,
				)
			}
		})
	}
}

func TestToolSandboxPreservesNonzeroProcessExitStatus(t *testing.T) {
	run, plan, _, executor := testRun(t)
	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects:         plan.Projects,
		ImageEnvironment: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatal(err)
	}

	executor.mu.Lock()
	executor.exitCode = 73
	executor.mu.Unlock()

	code, err := sandbox.Exec(
		t.Context(),
		[]string{"/bin/false"},
		sandboxapi.ExecOptions{Foreground: true},
	)
	if code != 73 || exitcode.FromError(err) != 73 {
		t.Fatalf("nonzero process result = code %d, error %v", code, err)
	}
}

func TestToolSandboxOpensProjectWhileForegroundCommandRuns(t *testing.T) {
	run, plan, _, _ := testRun(t)
	gate := &overlapGate{
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
	run.executor = &gatedProcessExecutor{
		name: "foreground",
		gate: gate,
	}

	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects:         plan.Projects,
		ImageEnvironment: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatal(err)
	}

	executionDone := make(chan error, 1)
	go func() {
		_, err := sandbox.Exec(
			t.Context(),
			[]string{"/bin/agent"},
			sandboxapi.ExecOptions{Foreground: true},
		)
		executionDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		close(gate.release)
		t.Fatal("foreground command did not reach the executor")
	}

	type openResult struct {
		directory *os.File
		err       error
	}
	opened := make(chan openResult, 1)
	go func() {
		directory, err := sandbox.OpenVisibleHostDirectory(
			plan.Projects[0].Name,
		)
		opened <- openResult{directory: directory, err: err}
	}()

	select {
	case result := <-opened:
		if result.err != nil {
			close(gate.release)
			<-executionDone
			t.Fatal(result.err)
		}
		if err := result.directory.Close(); err != nil {
			close(gate.release)
			<-executionDone
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(gate.release)
		<-executionDone
		t.Fatal("project resolution blocked behind the foreground command")
	}

	close(gate.release)
	if err := <-executionDone; err != nil {
		t.Fatal(err)
	}
}

func TestToolSandboxAttachesProjectlessPlan(t *testing.T) {
	plan := validPlan()
	plan.Projects = nil
	plan.ManagedDirectories = nil
	plan.GeneratedFiles = nil
	plan.Workdir = layout.Home
	plan.Environment = nil
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	run := &Run{plan: plan.Canonical()}
	sandbox, err := NewToolSandbox(ToolSandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatalf("attach projectless native run: %v", err)
	}
}

func TestToolSandboxRejectsMutationAfterAttach(t *testing.T) {
	run, plan, _, _ := testRun(t)
	sandbox, err := NewToolSandbox(ToolSandboxOptions{Projects: plan.Projects})
	if err != nil {
		t.Fatal(err)
	}
	err = sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SetEnvironment(t.Context(), "PATH", "/usr/bin:/bin"); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatal(err)
	}

	if err := sandbox.SetEnvironment(
		context.Background(),
		"AFTER",
		"attach",
	); err == nil {
		t.Fatal("environment mutation after attachment was accepted")
	}
	if err := sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "codex",
			Purpose: "state",
		},
		Target: "~/.codex",
	}); err == nil {
		t.Fatal("mount mutation after attachment was accepted")
	}
}

func TestToolSandboxOpensExactSelectedProjectDirectory(t *testing.T) {
	run, plan, sources, _ := testRun(t)
	project := plan.Projects[0]
	projectRoot := sources.Projects[project.Name].Name()
	visible := filepath.Join(projectRoot, "repository")
	retained := filepath.Join(projectRoot, "retained")
	outside := t.TempDir()
	if err := os.Mkdir(visible, 0o700); err != nil {
		t.Fatal(err)
	}

	sandbox, err := NewToolSandbox(ToolSandboxOptions{
		Projects: plan.Projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AddMount(mount.Request{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "opencode",
			Purpose: "state",
		},
		Target: "~/.local/share/opencode",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.SetEnvironment(
		t.Context(),
		"PATH",
		"/usr/bin:/bin",
	); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Attach(run); err != nil {
		t.Fatal(err)
	}

	directory, err := sandbox.OpenVisibleHostDirectory(
		project.Name + "/repository",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	if err := os.Rename(visible, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, visible); err != nil {
		t.Fatal(err)
	}

	openedInfo, err := directory.Stat()
	if err != nil {
		t.Fatal(err)
	}
	retainedInfo, err := os.Stat(retained)
	if err != nil {
		t.Fatal(err)
	}
	outsideInfo, err := os.Stat(visible)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(openedInfo, retainedInfo) {
		t.Fatal("opened repository descriptor was retargeted after rename")
	}
	if os.SameFile(openedInfo, outsideInfo) {
		t.Fatal("opened repository descriptor followed replacement symlink")
	}

	if _, err := sandbox.OpenVisibleHostDirectory(
		project.Name + "/repository",
	); err == nil {
		t.Fatal("replacement repository symlink was accepted")
	}
}
