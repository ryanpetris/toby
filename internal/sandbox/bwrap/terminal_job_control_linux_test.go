//go:build linux

package bwrap

// Exercises direct terminal job control through real nested PTYs.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	terminalJobControlHelperEnvironment = "TOBY_TERMINAL_JOB_CONTROL_HELPER"
	terminalJobControlParentEnvironment = "TOBY_TERMINAL_JOB_CONTROL_PARENT"
)

func TestDirectTerminalStopsAndContinuesWithChild(t *testing.T) {
	process := startTerminalJobControlHelper(t, "direct")
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

func TestDirectTerminalBackgroundExitPreservesShellForeground(t *testing.T) {
	process := startTerminalJobControlHelper(t, "direct-background")
	waitForTerminalMatch(
		t,
		process.output,
		`direct-background-owner-preserved`,
	)
	process.waitForExit(t)
}

func TestTerminalJobControlHelper(t *testing.T) {
	switch os.Getenv(terminalJobControlHelperEnvironment) {
	case "":
		return
	case "direct":
		runDirectTerminalJobControlHelper(t)
	case "direct-background":
		runDirectBackgroundJobControlHelper(t)
	case "controller-direct":
		runTerminalJobControlController(t)
	default:
		t.Fatalf(
			"unknown terminal job-control helper %q",
			os.Getenv(terminalJobControlHelperEnvironment),
		)
	}
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
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("direct terminal child status = %d, want 0", code)
	}
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
		nil,
		false,
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

func runTerminalJobControlController(t *testing.T) {
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
	<-time.After(24 * time.Hour)
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
