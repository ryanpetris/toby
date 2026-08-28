//go:build linux

package bwrap

// Owns foreground process-group transfer and suspend/resume coordination for
// direct host-terminal executions.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	childCodeStopped   = 5
	childCodeContinued = 6
)

func (e *Executor) executeDirectTerminal(
	ctx context.Context,
	command *exec.Cmd,
	invocation *Invocation,
	notifyStarted func(int),
	registerSignalHandler func(func(syscall.Signal) error) func(),
	payloadTarget *payloadSignalTarget,
	claimTerminal bool,
) (code int, returnErr error) {
	// claimedPayload is set only when the payload takes over the terminal
	// foreground; it then leads its own process group named by its host PID.
	var claimedPayload *payloadSignalTarget
	if claimTerminal {
		claimedPayload = payloadTarget
	}

	terminal, ok := command.Stdin.(*os.File)
	if !ok {
		return 1, fmt.Errorf("direct-terminal stdin is not a terminal file")
	}

	parentGroup := unix.Getpgrp()
	foregroundGroup, err := terminalForegroundGroup(terminal)
	if err != nil {
		return 1, fmt.Errorf(
			"read direct terminal foreground process group: %w",
			err,
		)
	}
	if foregroundGroup != parentGroup {
		return 1, fmt.Errorf(
			"direct terminal is owned by process group %d, want Toby process group %d",
			foregroundGroup,
			parentGroup,
		)
	}

	childChanged := make(chan os.Signal, 4)
	signal.Notify(childChanged, syscall.SIGCHLD)
	defer signal.Stop(childChanged)

	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Foreground: true,
		Ctty:       int(terminal.Fd()),
	}
	if err := command.Start(); err != nil {
		restoreErr := setTerminalForegroundGroup(terminal, parentGroup)
		return 1, errors.Join(
			fmt.Errorf("start Bubblewrap in terminal foreground: %w", err),
			restoreErr,
		)
	}
	if notifyStarted != nil {
		notifyStarted(command.Process.Pid)
	}
	group, err := retainStartedProcessGroup(command, invocation)
	if err != nil {
		restoreErr := setTerminalForegroundGroup(terminal, parentGroup)
		return 1, errors.Join(err, restoreErr)
	}
	defer func() {
		e.logger.DebugError(
			"close direct-terminal process-group identity",
			group.Close(),
		)
	}()

	e.logger.DebugError(
		"close direct-terminal Bubblewrap invocation",
		invocation.Close(),
	)

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	waitErr, controlErr := e.waitForDirectTerminal(
		ctx,
		group,
		claimedPayload,
		terminal,
		parentGroup,
		wait,
		childChanged,
		registerSignalHandler,
	)
	restoreErr := restoreDirectTerminalForeground(
		terminal,
		group.PID(),
		claimedPayload.HostPID(),
		parentGroup,
	)
	code, resultErr := childResult(waitErr)

	return code, errors.Join(
		controlErr,
		restoreErr,
		resultErr,
	)
}

func restoreDirectTerminalForeground(
	terminal *os.File,
	childGroup int,
	payloadGroup int,
	parentGroup int,
) error {
	foregroundGroup, err := terminalForegroundGroup(terminal)
	if err != nil {
		return fmt.Errorf(
			"read terminal foreground process group after child exit: %w",
			err,
		)
	}
	if foregroundGroup != childGroup &&
		(payloadGroup <= 0 || foregroundGroup != payloadGroup) {
		return nil
	}
	return setTerminalForegroundGroup(terminal, parentGroup)
}

func (e *Executor) waitForDirectTerminal(
	ctx context.Context,
	group *processGroupIdentity,
	claimedPayload *payloadSignalTarget,
	terminal *os.File,
	parentGroup int,
	wait <-chan error,
	childChanged <-chan os.Signal,
	registerSignalHandler func(func(syscall.Signal) error) func(),
) (waitErr error, returnErr error) {
	forwarded := make(chan os.Signal, 8)
	localSignals := []os.Signal{
		syscall.SIGHUP,
		syscall.SIGQUIT,
		syscall.SIGTSTP,
	}
	if !e.externalInterrupts {
		localSignals = append(
			localSignals,
			syscall.SIGINT,
			syscall.SIGTERM,
		)
	}
	signal.Notify(forwarded, localSignals...)
	defer signal.Stop(forwarded)
	unregister := registerProcessSignalHandler(
		registerSignalHandler,
		group,
	)
	defer unregister()

	observeState := func() error {
		if err := handleDirectChildStateChange(
			group,
			terminal,
			parentGroup,
		); err != nil {
			return err
		}
		return handleDirectPayloadStateChange(
			claimedPayload,
			terminal,
			parentGroup,
		)
	}

	statePoll := time.NewTicker(25 * time.Millisecond)
	defer statePoll.Stop()

	for {
		select {
		case waitErr = <-wait:
			return waitErr, returnErr
		case current := <-forwarded:
			currentSignal, ok := current.(syscall.Signal)
			if !ok {
				continue
			}
			if err := group.Signal(currentSignal); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
			if err := claimedPayload.SignalGroup(currentSignal); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		case <-childChanged:
			if err := observeState(); err != nil {
				terminated := e.terminateDirectTerminal(
					group,
					claimedPayload,
					wait,
				)
				return terminated.waitErr, errors.Join(
					returnErr,
					err,
					terminated.signalErr,
				)
			}
		case <-statePoll.C:
			if err := observeState(); err != nil {
				terminated := e.terminateDirectTerminal(
					group,
					claimedPayload,
					wait,
				)
				return terminated.waitErr, errors.Join(
					returnErr,
					err,
					terminated.signalErr,
				)
			}
		case <-ctx.Done():
			terminated := e.terminateDirectTerminal(
				group,
				claimedPayload,
				wait,
			)
			return terminated.waitErr, errors.Join(
				returnErr,
				ctx.Err(),
				terminated.signalErr,
			)
		}
	}
}

// terminateDirectTerminal sends the graceful termination signal to a payload
// that owns its own foreground process group before tearing down the
// Bubblewrap group, which no longer contains that payload.
func (e *Executor) terminateDirectTerminal(
	group *processGroupIdentity,
	claimedPayload *payloadSignalTarget,
	wait <-chan error,
) terminationResult {
	payloadErr := claimedPayload.SignalGroup(syscall.SIGTERM)
	result := e.terminateCommand(group, wait)
	result.signalErr = errors.Join(payloadErr, result.signalErr)
	return result
}

func handleDirectChildStateChange(
	group *processGroupIdentity,
	terminal *os.File,
	parentGroup int,
) error {
	code, changed, err := childStateChange(group)
	if err != nil || !changed {
		return err
	}
	switch code {
	case childCodeContinued:
		return nil
	case childCodeStopped:
		if err := suspendDirectTerminal(
			group,
			terminal,
			parentGroup,
		); err != nil {
			return err
		}
		return nil
	default:
		return nil
	}
}

func childStateChange(
	group *processGroupIdentity,
) (code int32, changed bool, returnErr error) {
	var info unix.Siginfo
	err := group.Waitid(
		&info,
		unix.WSTOPPED|unix.WCONTINUED|unix.WNOHANG,
	)
	if errors.Is(err, unix.ECHILD) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf(
			"inspect terminal child %d state: %w",
			group.PID(),
			err,
		)
	}
	if info.Signo == 0 {
		return 0, false, nil
	}
	return info.Code, true, nil
}

func suspendDirectTerminal(
	group *processGroupIdentity,
	terminal *os.File,
	parentGroup int,
) error {
	foregroundGroup, err := terminalForegroundGroup(terminal)
	if err != nil {
		return fmt.Errorf("inspect terminal before suspension: %w", err)
	}
	if foregroundGroup == group.PID() {
		if err := setTerminalForegroundGroup(
			terminal,
			parentGroup,
		); err != nil {
			return fmt.Errorf("reclaim terminal before suspension: %w", err)
		}
	}
	if err := stopProcessGroup(parentGroup); err != nil {
		return err
	}
	return resumeDirectTerminal(group, terminal, parentGroup)
}

func resumeDirectTerminal(
	group *processGroupIdentity,
	terminal *os.File,
	parentGroup int,
) error {
	foregroundGroup, foregroundErr := terminalForegroundGroup(terminal)
	if foregroundErr == nil && foregroundGroup == parentGroup {
		foregroundErr = setTerminalForegroundGroup(
			terminal,
			group.PID(),
		)
	}
	if foregroundErr != nil {
		signalErr := group.Signal(syscall.SIGCONT)
		return errors.Join(
			fmt.Errorf(
				"return terminal to resumed child: %w",
				foregroundErr,
			),
			signalErr,
		)
	}
	if err := group.Signal(syscall.SIGCONT); err != nil {
		return fmt.Errorf("continue resumed terminal child: %w", err)
	}
	return nil
}

// handleDirectPayloadStateChange coordinates suspension for a payload that
// owns the terminal foreground in its own process group. The payload is not a
// child of this process, so job-control stops are observed through its
// procfs state and resumed through its retained pidfd.
func handleDirectPayloadStateChange(
	claimedPayload *payloadSignalTarget,
	terminal *os.File,
	parentGroup int,
) error {
	payloadGroup := claimedPayload.HostPID()
	if payloadGroup <= 0 {
		return nil
	}

	stopped, err := payloadGroupStopped(payloadGroup)
	if err != nil || !stopped {
		return err
	}
	foregroundGroup, err := terminalForegroundGroup(terminal)
	if err != nil {
		return fmt.Errorf(
			"inspect terminal before payload suspension: %w",
			err,
		)
	}
	if foregroundGroup != payloadGroup {
		return nil
	}

	if err := setTerminalForegroundGroup(terminal, parentGroup); err != nil {
		return fmt.Errorf(
			"reclaim terminal from stopped payload: %w",
			err,
		)
	}
	if err := stopProcessGroup(parentGroup); err != nil {
		return err
	}
	return resumeDirectPayloadTerminal(
		claimedPayload,
		terminal,
		payloadGroup,
		parentGroup,
	)
}

func resumeDirectPayloadTerminal(
	claimedPayload *payloadSignalTarget,
	terminal *os.File,
	payloadGroup int,
	parentGroup int,
) error {
	foregroundGroup, foregroundErr := terminalForegroundGroup(terminal)
	if foregroundErr == nil && foregroundGroup == parentGroup {
		foregroundErr = setTerminalForegroundGroup(terminal, payloadGroup)
	}
	continueErr := claimedPayload.SignalGroup(syscall.SIGCONT)
	if foregroundErr != nil {
		foregroundErr = fmt.Errorf(
			"return terminal to resumed payload: %w",
			foregroundErr,
		)
	}
	return errors.Join(foregroundErr, continueErr)
}

// payloadGroupStopped reports whether the payload group leader is in the
// job-control stopped state. A missing process reads as not stopped; the exit
// path owns that condition.
func payloadGroupStopped(pid int) (bool, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"inspect payload group leader %d state: %w",
			pid,
			err,
		)
	}

	// The single-character state field follows the parenthesized command name.
	end := bytes.LastIndexByte(data, ')')
	if end < 0 || end+2 >= len(data) {
		return false, fmt.Errorf(
			"payload group leader %d state record is malformed",
			pid,
		)
	}
	return data[end+2] == 'T', nil
}

func terminalForegroundGroup(terminal *os.File) (int, error) {
	if terminal == nil {
		return 0, fmt.Errorf("terminal is nil")
	}
	group, err := unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0, err
	}
	return group, nil
}

func stopProcessGroup(processGroup int) error {
	if processGroup <= 0 {
		return fmt.Errorf("invalid process group %d", processGroup)
	}
	continued := make(chan os.Signal, 1)
	signal.Notify(continued, syscall.SIGCONT)
	defer signal.Stop(continued)

	if err := syscall.Kill(-processGroup, syscall.SIGSTOP); err != nil {
		return fmt.Errorf("stop terminal process group %d: %w", processGroup, err)
	}
	<-continued
	return nil
}

func setTerminalForegroundGroup(terminal *os.File, processGroup int) error {
	if terminal == nil || processGroup <= 0 {
		return fmt.Errorf("invalid terminal foreground process group %d", processGroup)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var blocked unix.Sigset_t
	for index := range blocked.Val {
		blocked.Val[index] = ^blocked.Val[index]
	}
	var original unix.Sigset_t
	if err := unix.PthreadSigmask(
		unix.SIG_SETMASK,
		&blocked,
		&original,
	); err != nil {
		return fmt.Errorf("block terminal foreground signals: %w", err)
	}
	setErr := unix.IoctlSetPointerInt(
		int(terminal.Fd()),
		unix.TIOCSPGRP,
		processGroup,
	)
	restoreErr := unix.PthreadSigmask(
		unix.SIG_SETMASK,
		&original,
		nil,
	)
	if setErr != nil {
		setErr = fmt.Errorf(
			"set terminal foreground process group %d: %w",
			processGroup,
			setErr,
		)
	}
	if restoreErr != nil {
		restoreErr = fmt.Errorf(
			"restore terminal signal mask: %w",
			restoreErr,
		)
	}
	return errors.Join(setErr, restoreErr)
}
