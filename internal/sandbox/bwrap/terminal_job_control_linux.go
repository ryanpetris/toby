//go:build linux

package bwrap

// Owns foreground process-group transfer and suspend/resume coordination for
// direct and managed host-terminal executions.

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
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	childCodeStopped          = 5
	childCodeContinued        = 6
	managedSuspendHandlerTime = 20 * time.Millisecond
	managedStopObserveTimeout = 250 * time.Millisecond
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

type managedTerminalState struct {
	mu sync.Mutex

	input       *os.File
	output      *os.File
	master      *os.File
	group       *processGroupIdentity
	parentGroup int

	original      *term.State
	raw           bool
	closed        bool
	suspend       byte
	suspendActive bool
}

func newManagedTerminalState(
	input, output, master *os.File,
	group *processGroupIdentity,
) (*managedTerminalState, error) {
	parentGroup := unix.Getpgrp()
	foregroundGroup, err := terminalForegroundGroup(input)
	if err != nil {
		return nil, fmt.Errorf(
			"read managed terminal foreground process group: %w",
			err,
		)
	}
	if foregroundGroup != parentGroup {
		return nil, fmt.Errorf(
			"managed terminal is owned by process group %d, want Toby process group %d",
			foregroundGroup,
			parentGroup,
		)
	}

	suspend, enabled, err := terminalSuspendCharacter(input)
	if err != nil {
		return nil, err
	}
	if err := setPTYSuspendCharacter(master, suspend, enabled); err != nil {
		return nil, err
	}
	original, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return nil, fmt.Errorf("put host terminal in raw mode: %w", err)
	}

	return &managedTerminalState{
		input:         input,
		output:        output,
		master:        master,
		group:         group,
		parentGroup:   parentGroup,
		original:      original,
		raw:           true,
		suspend:       suspend,
		suspendActive: enabled,
	}, nil
}

func (s *managedTerminalState) SuspendCharacter() (byte, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false
	}
	return s.suspend, s.suspendActive
}

func (s *managedTerminalState) Suspend() (
	width int,
	height int,
	foreground bool,
	returnErr error,
) {
	if s == nil {
		return 0, 0, false, fmt.Errorf("managed terminal state is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, 0, false, os.ErrClosed
	}
	if !s.raw || s.original == nil {
		return 0, 0, false, fmt.Errorf("managed terminal is not raw")
	}

	if err := term.Restore(int(s.input.Fd()), s.original); err != nil {
		return 0, 0, false, fmt.Errorf(
			"restore host terminal before suspension: %w",
			err,
		)
	}
	s.raw = false

	if err := stopManagedTerminalChild(s.group); err != nil {
		return 0, 0, false, err
	}
	if err := stopProcessGroup(s.parentGroup); err != nil {
		return 0, 0, false, err
	}
	return s.prepareResumeLocked()
}

func stopManagedTerminalChild(group *processGroupIdentity) error {
	tstpErr := group.Signal(syscall.SIGTSTP)
	stopped, observeErr := waitForManagedTerminalChildStop(
		group,
		managedSuspendHandlerTime,
	)
	if observeErr != nil || stopped {
		return errors.Join(tstpErr, observeErr)
	}

	stopErr := group.Signal(syscall.SIGSTOP)
	stopped, observeErr = waitForManagedTerminalChildStop(
		group,
		managedStopObserveTimeout,
	)
	if observeErr == nil && !stopped {
		observeErr = fmt.Errorf(
			"managed terminal child %d did not stop within %s",
			group.PID(),
			managedStopObserveTimeout,
		)
	}
	return errors.Join(tstpErr, stopErr, observeErr)
}

func waitForManagedTerminalChildStop(
	group *processGroupIdentity,
	timeout time.Duration,
) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		var info unix.Siginfo
		err := group.Waitid(
			&info,
			unix.WSTOPPED|unix.WNOHANG,
		)
		if errors.Is(err, unix.ECHILD) {
			return false, fmt.Errorf(
				"managed terminal child %d exited before stopping",
				group.PID(),
			)
		}
		if err != nil {
			return false, fmt.Errorf(
				"inspect managed terminal child %d stop: %w",
				group.PID(),
				err,
			)
		}
		if info.Signo != 0 && info.Code == childCodeStopped {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *managedTerminalState) prepareResumeLocked() (
	width int,
	height int,
	foreground bool,
	returnErr error,
) {
	for {
		foregroundGroup, foregroundErr := terminalForegroundGroup(s.input)
		if foregroundErr != nil {
			return 0, 0, false, fmt.Errorf(
				"read resumed terminal foreground process group: %w",
				foregroundErr,
			)
		}
		if foregroundGroup == s.parentGroup {
			break
		}
		if err := stopProcessGroup(s.parentGroup); err != nil {
			return 0, 0, false, fmt.Errorf(
				"restop background managed terminal: %w",
				err,
			)
		}
	}

	suspend, enabled, suspendErr := terminalSuspendCharacter(s.input)
	original, rawErr := term.MakeRaw(int(s.input.Fd()))
	if rawErr == nil {
		s.original = original
		s.raw = true
		s.suspend = suspend
		s.suspendActive = enabled
	}
	ptyErr := setPTYSuspendCharacter(s.master, suspend, enabled)
	width, height = terminalSize(s.output)
	resizeErr := setManagedPTYSize(s.master, width, height)

	return width, height, true, errors.Join(
		suspendErr,
		rawErr,
		ptyErr,
		resizeErr,
	)
}

func (s *managedTerminalState) Resume() error {
	if s == nil {
		return fmt.Errorf("managed terminal state is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if err := s.group.Signal(syscall.SIGCONT); err != nil {
		return fmt.Errorf("continue managed terminal child: %w", err)
	}
	return nil
}

func (s *managedTerminalState) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.raw || s.original == nil {
		return nil
	}
	s.raw = false
	return term.Restore(int(s.input.Fd()), s.original)
}

func terminalSuspendCharacter(
	terminal *os.File,
) (character byte, enabled bool, returnErr error) {
	if terminal == nil {
		return 0, false, fmt.Errorf("host terminal is nil")
	}
	attributes, err := unix.IoctlGetTermios(
		int(terminal.Fd()),
		unix.TCGETS,
	)
	if err != nil {
		return 0, false, fmt.Errorf(
			"read host terminal control characters: %w",
			err,
		)
	}
	character = attributes.Cc[unix.VSUSP]
	return character, character != 0, nil
}

func setPTYSuspendCharacter(
	master *os.File,
	character byte,
	enabled bool,
) error {
	if master == nil {
		return fmt.Errorf("managed PTY is nil")
	}
	attributes, err := unix.IoctlGetTermios(
		int(master.Fd()),
		unix.TCGETS,
	)
	if err != nil {
		return fmt.Errorf("read managed PTY control characters: %w", err)
	}
	if !enabled {
		character = 0
	}
	attributes.Cc[unix.VSUSP] = character
	if err := unix.IoctlSetTermios(
		int(master.Fd()),
		unix.TCSETS,
		attributes,
	); err != nil {
		return fmt.Errorf("set managed PTY suspend character: %w", err)
	}
	return nil
}
