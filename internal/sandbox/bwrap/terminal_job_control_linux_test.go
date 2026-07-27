//go:build linux

package bwrap

// Exercises direct and managed terminal job control through real nested PTYs.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	sandboxapi "petris.dev/toby/internal/sandbox"
)

const (
	terminalJobControlHelperEnvironment = "TOBY_TERMINAL_JOB_CONTROL_HELPER"
	terminalJobControlParentEnvironment = "TOBY_TERMINAL_JOB_CONTROL_PARENT"
)

func TestDirectTerminalStopsAndContinuesWithChild(t *testing.T) {
	process := startTerminalJobControlHelper(t, "direct", nil)
	childMatch := waitForTerminalMatch(
		t,
		process.output,
		`direct-ready:([0-9]+):1`,
	)
	childPID, err := strconv.Atoi(childMatch[1])
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-childPID, syscall.SIGKILL)

	for pass := 1; pass <= 2; pass++ {
		if pass > 1 {
			waitForTerminalMatch(
				t,
				process.output,
				fmt.Sprintf(`direct-ready:%d:%d`, childPID, pass),
			)
		}
		waitForStoppedProcess(t, childPID)
		waitForStoppedProcess(t, process.command.Process.Pid)

		if err := syscall.Kill(
			-process.command.Process.Pid,
			syscall.SIGCONT,
		); err != nil {
			t.Fatal(err)
		}
		waitForTerminalMatch(
			t,
			process.output,
			fmt.Sprintf(`direct-resumed:%d`, pass),
		)
	}

	process.waitForExit(t)
}

func TestManagedTerminalStopsResizesAndContinuesWithChild(t *testing.T) {
	suspend := byte(0x19)
	process := startTerminalJobControlHelper(t, "managed", &suspend)
	childMatch := waitForTerminalMatch(
		t,
		process.output,
		`managed-ready:([0-9]+)`,
	)
	childPID, err := strconv.Atoi(childMatch[1])
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-childPID, syscall.SIGKILL)

	if _, err := process.master.Write([]byte{suspend}); err != nil {
		t.Fatal(err)
	}
	waitForStoppedProcess(t, childPID)
	waitForStoppedProcess(t, process.command.Process.Pid)

	controllerMatch := waitForTerminalMatch(
		t,
		process.output,
		`managed-background-restopped:([0-9]+)`,
	)
	controllerPID, err := strconv.Atoi(controllerMatch[1])
	if err != nil {
		t.Fatal(err)
	}
	waitForStoppedProcess(t, process.command.Process.Pid)
	if err := pty.Setsize(process.master, &pty.Winsize{
		Rows: 41,
		Cols: 103,
	}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(controllerPID, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitForTerminalMatch(
		t,
		process.output,
		"\x1b\\[\\?2026h",
	)
	if _, err := process.master.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}

	waitForTerminalMatch(
		t,
		process.output,
		`managed-resumed:41 103`,
	)
	process.waitForExit(t)
}

func TestManagedTerminalHandlesSignalsDuringRawModeSetup(t *testing.T) {
	process := startTerminalJobControlHelper(
		t,
		"managed-signal-window",
		nil,
	)
	waitForTerminalMatch(
		t,
		process.output,
		`managed-signal-window-ready`,
	)

	if err := syscall.Kill(
		process.command.Process.Pid,
		syscall.SIGTERM,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-process.wait:
		process.waited = true
		t.Fatalf(
			"managed terminal helper exited in raw mode: %v\noutput:\n%s",
			err,
			process.output.snapshot(),
		)
	case <-time.After(100 * time.Millisecond):
	}

	if err := syscall.Kill(
		process.command.Process.Pid,
		syscall.SIGUSR1,
	); err != nil {
		t.Fatal(err)
	}
	waitForTerminalMatch(
		t,
		process.output,
		`managed-signal-window-cleanup`,
	)

	if err := syscall.Kill(
		process.command.Process.Pid,
		syscall.SIGHUP,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-process.wait:
		process.waited = true
		t.Fatalf(
			"managed terminal helper exited before restoring its terminal: %v\noutput:\n%s",
			err,
			process.output.snapshot(),
		)
	case <-time.After(100 * time.Millisecond):
	}

	if err := syscall.Kill(
		process.command.Process.Pid,
		syscall.SIGUSR2,
	); err != nil {
		t.Fatal(err)
	}
	waitForTerminalMatch(
		t,
		process.output,
		`managed-signal-window-restored:143`,
	)
	process.waitForExit(t)
}

func TestDirectTerminalBackgroundExitPreservesShellForeground(t *testing.T) {
	process := startTerminalJobControlHelper(t, "direct-background", nil)
	waitForTerminalMatch(
		t,
		process.output,
		`direct-background-owner-preserved`,
	)
	process.waitForExit(t)
}

func TestTerminalSuspendCharacterConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		character byte
		enabled   bool
	}{
		{name: "custom", character: 0x19, enabled: true},
		{name: "disabled", character: 0, enabled: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer terminal.Close()

			if err := setTerminalTestSuspendCharacter(
				terminal,
				test.character,
			); err != nil {
				t.Fatal(err)
			}
			character, enabled, err := terminalSuspendCharacter(terminal)
			if err != nil {
				t.Fatal(err)
			}
			if character != test.character || enabled != test.enabled {
				t.Fatalf(
					"suspend character = %#x, %t; want %#x, %t",
					character,
					enabled,
					test.character,
					test.enabled,
				)
			}

			replacement := byte(0x1c)
			if !test.enabled {
				replacement = 0
			}
			if err := setPTYSuspendCharacter(
				master,
				replacement,
				test.enabled,
			); err != nil {
				t.Fatal(err)
			}
			attributes, err := unix.IoctlGetTermios(
				int(terminal.Fd()),
				unix.TCGETS,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := attributes.Cc[unix.VSUSP]; got != replacement {
				t.Fatalf(
					"managed PTY suspend character = %#x, want %#x",
					got,
					replacement,
				)
			}
		})
	}
}

func TestTerminalJobControlHelper(t *testing.T) {
	switch os.Getenv(terminalJobControlHelperEnvironment) {
	case "":
		return
	case "direct":
		runDirectTerminalJobControlHelper(t)
	case "managed":
		runManagedTerminalJobControlHelper(t)
	case "managed-signal-window":
		runManagedTerminalSignalWindowHelper(t)
	case "direct-background":
		runDirectBackgroundJobControlHelper(t)
	case "controller-direct":
		runTerminalJobControlController(t, false)
	case "controller-managed":
		runTerminalJobControlController(t, true)
	default:
		t.Fatalf(
			"unknown terminal job-control helper %q",
			os.Getenv(terminalJobControlHelperEnvironment),
		)
	}
}

func runManagedTerminalSignalWindowHelper(t *testing.T) {
	original, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan os.Signal, 2)
	signal.Notify(release, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(release)

	command := exec.Command("/bin/sh", "-c", "sleep 30")
	executor := &Executor{terminationGrace: 50 * time.Millisecond}
	code, err := executor.executeManagedPTY(
		t.Context(),
		command,
		ProcessIO{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
			RegisterPrompter: func(
				prompter sandboxapi.ApprovalPrompter,
			) {
				if prompter == nil {
					fmt.Println("managed-signal-window-cleanup")
					<-release
					return
				}
				fmt.Println("managed-signal-window-ready")
				<-release
			},
		},
		&Invocation{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != 128+int(syscall.SIGTERM) {
		t.Fatalf(
			"managed terminal child status = %d, want %d",
			code,
			128+int(syscall.SIGTERM),
		)
	}

	restored, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatal("managed terminal state was not restored after signal")
	}
	fmt.Printf("managed-signal-window-restored:%d\n", code)
}

func runDirectTerminalJobControlHelper(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", directJobControlScriptFixture)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	executor := &Executor{terminationGrace: 50 * time.Millisecond}
	code, err := executor.executeDirectTerminal(
		t.Context(),
		command,
		&Invocation{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("direct terminal child status = %d, want 0", code)
	}
}

func runManagedTerminalJobControlHelper(t *testing.T) {
	controller := startTerminalJobControlController(t, "controller-managed")
	defer func() {
		stopTerminalJobControlController(controller)
	}()

	command := exec.Command("/bin/sh", "-c", managedJobControlScriptFixture)

	executor := &Executor{terminationGrace: 50 * time.Millisecond}
	code, err := executor.executeManagedPTY(
		t.Context(),
		command,
		ProcessIO{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		},
		&Invocation{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("managed terminal child status = %d, want 0", code)
	}
	if err := controller.Wait(); err != nil {
		t.Fatalf("managed job-control controller: %v", err)
	}
	controller = nil
}

func runDirectBackgroundJobControlHelper(t *testing.T) {
	controller := startTerminalJobControlController(t, "controller-direct")
	defer func() {
		stopTerminalJobControlController(controller)
	}()

	command := exec.Command("/bin/sh", "-c", backgroundJobControlScriptFixture)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	executor := &Executor{terminationGrace: 50 * time.Millisecond}
	code, err := executor.executeDirectTerminal(
		t.Context(),
		command,
		&Invocation{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("direct background child status = %d, want 0", code)
	}
	foregroundGroup, err := terminalForegroundGroup(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if foregroundGroup != controller.Process.Pid {
		t.Fatalf(
			"terminal foreground group = %d, want shell controller %d",
			foregroundGroup,
			controller.Process.Pid,
		)
	}

	if err := setTerminalForegroundGroup(os.Stdin, unix.Getpgrp()); err != nil {
		t.Fatal(err)
	}
	stopTerminalJobControlController(controller)
	controller = nil
	fmt.Println("direct-background-owner-preserved")
}

func startTerminalJobControlController(
	t *testing.T,
	helper string,
) *exec.Cmd {
	t.Helper()

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestTerminalJobControlHelper$",
	)
	command.Env = terminalJobControlHelperEnvironmentFor(
		helper,
		strconv.Itoa(os.Getpid()),
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

func stopTerminalJobControlController(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Wait()
}

func runTerminalJobControlController(t *testing.T, managed bool) {
	parentPID, err := strconv.Atoi(
		os.Getenv(terminalJobControlParentEnvironment),
	)
	if err != nil || parentPID <= 0 {
		t.Fatalf(
			"invalid terminal job-control parent %q",
			os.Getenv(terminalJobControlParentEnvironment),
		)
	}

	waitForStoppedProcess(t, parentPID)
	if err := setTerminalForegroundGroup(os.Stdin, unix.Getpgrp()); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-parentPID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if !managed {
		<-time.After(24 * time.Hour)
		return
	}

	time.Sleep(50 * time.Millisecond)
	waitForStoppedProcess(t, parentPID)
	foreground := make(chan os.Signal, 1)
	signal.Notify(foreground, syscall.SIGUSR1)
	defer signal.Stop(foreground)
	fmt.Printf("managed-background-restopped:%d\n", os.Getpid())
	select {
	case <-foreground:
	case <-time.After(3 * time.Second):
		t.Fatal("managed foreground continuation was not requested")
	}
	if err := setTerminalForegroundGroup(os.Stdin, parentPID); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-parentPID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
}

type terminalJobControlProcess struct {
	command *exec.Cmd
	master  *os.File
	output  *terminalTestOutput
	wait    chan error
	waited  bool
}

func startTerminalJobControlHelper(
	t *testing.T,
	helper string,
	suspend *byte,
) *terminalJobControlProcess {
	t.Helper()

	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{
		Rows: 24,
		Cols: 80,
	}); err != nil {
		master.Close()
		terminal.Close()
		t.Fatal(err)
	}
	if suspend != nil {
		if err := setTerminalTestSuspendCharacter(
			terminal,
			*suspend,
		); err != nil {
			master.Close()
			terminal.Close()
			t.Fatal(err)
		}
	}

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestTerminalJobControlHelper$",
	)
	command.Env = terminalJobControlHelperEnvironmentFor(helper, "")
	command.Stdin = terminal
	command.Stdout = terminal
	command.Stderr = terminal
	command.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	if err := command.Start(); err != nil {
		master.Close()
		terminal.Close()
		t.Fatal(err)
	}
	if err := terminal.Close(); err != nil {
		syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		master.Close()
		command.Wait()
		t.Fatal(err)
	}

	process := &terminalJobControlProcess{
		command: command,
		master:  master,
		output:  newTerminalTestOutput(master),
		wait:    make(chan error, 1),
	}
	go func() {
		process.wait <- command.Wait()
	}()
	t.Cleanup(func() {
		if process.waited {
			_ = master.Close()
			return
		}
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = master.Close()
		select {
		case <-process.wait:
			process.waited = true
		case <-time.After(time.Second):
			t.Errorf(
				"terminal helper %d did not exit during cleanup",
				command.Process.Pid,
			)
		}
	})

	return process
}

func terminalJobControlHelperEnvironmentFor(
	helper string,
	parent string,
) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(
			variable,
			terminalJobControlHelperEnvironment+"=",
		) || strings.HasPrefix(
			variable,
			terminalJobControlParentEnvironment+"=",
		) {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(
		environment,
		terminalJobControlHelperEnvironment+"="+helper,
	)
	if parent != "" {
		environment = append(
			environment,
			terminalJobControlParentEnvironment+"="+parent,
		)
	}
	return environment
}

func (p *terminalJobControlProcess) waitForExit(t *testing.T) {
	t.Helper()

	select {
	case err := <-p.wait:
		p.waited = true
		if err != nil {
			t.Fatalf(
				"terminal helper failed: %v\noutput:\n%s",
				err,
				p.output.snapshot(),
			)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf(
			"terminal helper did not exit\noutput:\n%s",
			p.output.snapshot(),
		)
	}
}

type terminalTestOutput struct {
	mu      sync.Mutex
	data    bytes.Buffer
	changed chan struct{}
}

func newTerminalTestOutput(master *os.File) *terminalTestOutput {
	output := &terminalTestOutput{changed: make(chan struct{}, 1)}
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, err := master.Read(buffer)
			if count > 0 {
				output.mu.Lock()
				_, _ = output.data.Write(buffer[:count])
				output.mu.Unlock()
				select {
				case output.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return output
}

func (o *terminalTestOutput) snapshot() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.String()
}

func waitForTerminalMatch(
	t *testing.T,
	output *terminalTestOutput,
	pattern string,
) []string {
	t.Helper()

	expression := regexp.MustCompile(pattern)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		if match := expression.FindStringSubmatch(output.snapshot()); match != nil {
			return match
		}
		select {
		case <-output.changed:
		case <-timer.C:
			t.Fatalf(
				"terminal output did not match %q\noutput:\n%s",
				pattern,
				output.snapshot(),
			)
		}
	}
}

func waitForStoppedProcess(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err == nil {
			for _, line := range strings.Split(string(status), "\n") {
				if strings.HasPrefix(line, "State:") &&
					strings.Contains(line, "T") {
					return
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process %d did not stop", pid)
}

func setTerminalTestSuspendCharacter(
	terminal *os.File,
	character byte,
) error {
	attributes, err := unix.IoctlGetTermios(
		int(terminal.Fd()),
		unix.TCGETS,
	)
	if err != nil {
		return err
	}
	attributes.Cc[unix.VSUSP] = character
	return unix.IoctlSetTermios(
		int(terminal.Fd()),
		unix.TCSETS,
		attributes,
	)
}
