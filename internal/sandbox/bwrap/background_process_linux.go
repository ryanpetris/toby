//go:build linux

package bwrap

// Starts and supervises noninteractive Bubblewrap processes without claiming
// terminal state or subscribing to host signals.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"

	"petris.dev/toby/internal/diagnostic"
)

type backgroundProcess struct {
	mu sync.Mutex

	monitor *processIdentity
	init    *processIdentity
	payload *processIdentity
	done    chan struct{}
	err     error
}

var _ BackgroundProcess = (*backgroundProcess)(nil)

type backgroundStatusResult struct {
	status bubblewrapStatus
	err    error
}

type backgroundChildResult struct {
	pid int
	err error
}

// StartBackground starts one noninteractive invocation and transfers ownership
// of invocation to the executor. Each stream must be unset or hold a non-nil
// direct *os.File so process reaping never depends on an os/exec stream-copy
// goroutine. The caller owns those descriptors and any external pumps attached
// to them; StartBackground does not close or supervise either. The returned
// process is independent of ctx after startup; its lifecycle owner must call
// Stop or Kill. When setup is non-nil, Bubblewrap holds the payload behind a
// launch gate while setup receives the retained init PID. StartBackground
// releases the gate only after setup succeeds.
func (e *Executor) StartBackground(
	ctx context.Context,
	invocation *Invocation,
	streams ProcessIO,
	setup BackgroundSetup,
) (result BackgroundProcess, returnErr error) {
	var logger *diagnostic.Logger
	if e != nil {
		logger = e.logger
	}
	if invocation != nil {
		defer func() {
			if invocation != nil {
				logger.DebugError(
					"close background Bubblewrap invocation",
					invocation.Close(),
				)
			}
		}()
	}
	if e == nil {
		return nil, fmt.Errorf("bubblewrap executor is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("start background Bubblewrap: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBackgroundInvocation(invocation, streams); err != nil {
		return nil, err
	}

	attempt, err := duplicateInvocation(invocation)
	invocationErr := invocation.Close()
	invocation = nil
	logger.DebugError(
		"close duplicated background Bubblewrap source invocation",
		invocationErr,
	)
	if err != nil {
		if attempt != nil {
			logger.DebugError(
				"close incomplete background Bubblewrap invocation",
				attempt.Close(),
			)
		}
		return nil, err
	}
	attemptOpen := true
	defer func() {
		if attemptOpen {
			logger.DebugError(
				"close background Bubblewrap attempt",
				attempt.Close(),
			)
		}
	}()

	executable, err := e.executablePath()
	if err != nil {
		return nil, err
	}

	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf(
			"create background Bubblewrap status pipe: %w",
			err,
		)
	}
	statusReaderOpen := true
	statusWriterOpen := true
	var gateReader *os.File
	var gateWriter *os.File
	gateReaderOpen := false
	gateWriterOpen := false
	defer func() {
		if statusReaderOpen {
			logger.DebugError(
				"close background Bubblewrap status reader",
				statusReader.Close(),
			)
		}
		if statusWriterOpen {
			logger.DebugError(
				"close background Bubblewrap status writer",
				statusWriter.Close(),
			)
		}
		if gateReaderOpen {
			logger.DebugError(
				"close background Bubblewrap launch gate reader",
				gateReader.Close(),
			)
		}
		if gateWriterOpen {
			logger.DebugError(
				"close background Bubblewrap launch gate writer",
				gateWriter.Close(),
			)
		}
	}()

	files := append([]*os.File(nil), attempt.ExtraFiles...)
	statusFD := childExtraFileBaseFD + len(files)
	files = append(files, statusWriter)

	controlArgs := []string{
		"--json-status-fd", strconv.Itoa(statusFD),
	}
	if setup != nil {
		gateReader, gateWriter, err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf(
				"create background Bubblewrap launch gate: %w",
				err,
			)
		}
		gateReaderOpen = true
		gateWriterOpen = true

		gateFD := childExtraFileBaseFD + len(files)
		files = append(files, gateReader)
		controlArgs = append(
			controlArgs,
			"--block-fd", strconv.Itoa(gateFD),
		)
	}

	args := append([]string(nil), attempt.Args...)
	args = append(controlArgs, args...)
	command := exec.Command(executable, args...)
	command.ExtraFiles = files
	command.Env = []string{}
	command.Stdin = streams.Stdin
	command.Stdout = streams.Stdout
	command.Stderr = streams.Stderr
	configureBackgroundCommand(command)
	if err := e.startBackgroundCommand(ctx, command); err != nil {
		return nil, fmt.Errorf("start background Bubblewrap: %w", err)
	}

	childResult := make(chan backgroundChildResult, 1)
	statusResult := make(chan backgroundStatusResult, 1)
	go func(reader *os.File) {
		childPublished := false
		status, err := readBubblewrapStatusEvents(
			reader,
			func(child bubblewrapChildStatus) {
				childPublished = true
				childResult <- backgroundChildResult{pid: child.pid}
			},
		)
		logger.DebugError(
			"close background Bubblewrap status reader",
			reader.Close(),
		)
		if !childPublished {
			if err == nil {
				err = fmt.Errorf(
					"bubblewrap status closed before reporting its init process",
				)
			}
			childResult <- backgroundChildResult{err: err}
		}
		statusResult <- backgroundStatusResult{status: status, err: err}
	}(statusReader)
	statusReaderOpen = false

	writerErr := statusWriter.Close()
	statusWriterOpen = false
	var gateReaderErr error
	if gateReaderOpen {
		gateReaderErr = gateReader.Close()
		gateReaderOpen = false
	}
	attemptErr := attempt.Close()
	attemptOpen = false
	logger.DebugError(
		"close launched background Bubblewrap attempt",
		attemptErr,
	)

	monitor, monitorErr := openProcessIdentity(
		command.Process.Pid,
		os.Getpid(),
	)
	startErr := errors.Join(
		writerErr,
		gateReaderErr,
		monitorErr,
		ctx.Err(),
	)
	if startErr != nil {
		cleanupErr := terminateStartedBackground(
			command,
			monitor,
			statusResult,
		)
		logger.DebugError(
			"terminate background Bubblewrap after startup failure",
			cleanupErr,
		)
		return nil, fmt.Errorf(
			"retain background Bubblewrap process: %w",
			startErr,
		)
	}

	var child backgroundChildResult
	select {
	case child = <-childResult:
	case <-ctx.Done():
		cleanupErr := terminateStartedBackground(
			command,
			monitor,
			statusResult,
		)
		logger.DebugError(
			"terminate background Bubblewrap after canceled startup",
			cleanupErr,
		)
		return nil, ctx.Err()
	}
	if child.err != nil {
		cleanupErr := terminateStartedBackground(
			command,
			monitor,
			statusResult,
		)
		logger.DebugError(
			"terminate background Bubblewrap after init discovery failure",
			cleanupErr,
		)
		return nil, fmt.Errorf(
			"retain background Bubblewrap init: %w",
			child.err,
		)
	}

	init, err := openProcessIdentity(child.pid, monitor.pid)
	if err != nil {
		cleanupErr := terminateStartedBackground(
			command,
			monitor,
			statusResult,
		)
		logger.DebugError(
			"terminate background Bubblewrap after init retention failure",
			cleanupErr,
		)
		return nil, fmt.Errorf(
			"retain background Bubblewrap init: %w",
			err,
		)
	}
	if setup != nil {
		setupErr := errors.Join(setup(ctx, child.pid), ctx.Err())
		if setupErr != nil {
			cleanupErr := terminateStartedBackground(
				command,
				monitor,
				statusResult,
				init,
			)
			logger.DebugError(
				"terminate background Bubblewrap after setup failure",
				cleanupErr,
			)
			return nil, fmt.Errorf(
				"prepare background Bubblewrap sandbox: %w",
				setupErr,
			)
		}

		gateErr := gateWriter.Close()
		gateWriterOpen = false
		if gateErr != nil {
			cleanupErr := terminateStartedBackground(
				command,
				monitor,
				statusResult,
				init,
			)
			logger.DebugError(
				"terminate background Bubblewrap after launch gate failure",
				cleanupErr,
			)
			return nil, fmt.Errorf(
				"release background Bubblewrap launch gate: %w",
				gateErr,
			)
		}
	}

	payload, payloadErr := retainBackgroundPayload(ctx, init)
	startErr = errors.Join(payloadErr, ctx.Err())
	if startErr != nil {
		cleanupErr := terminateStartedBackground(
			command,
			monitor,
			statusResult,
			payload,
			init,
		)
		logger.DebugError(
			"terminate background Bubblewrap after payload retention failure",
			cleanupErr,
		)
		return nil, fmt.Errorf(
			"retain background Bubblewrap payload: %w",
			startErr,
		)
	}

	process := &backgroundProcess{
		monitor: monitor,
		init:    init,
		payload: payload,
		done:    make(chan struct{}),
	}
	go process.wait(command, statusResult)

	return process, nil
}

func configureBackgroundCommand(command *exec.Cmd) {
	discardNonInteractiveTerminalInput(command)

	// Cover owner death between fork and Bubblewrap's own parent-death setup.
	// Setsid also prevents agent-owned sidecars from inheriting any controlling
	// terminal held by the agent.
	command.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
		Setsid:    true,
	}
}

func validateBackgroundInvocation(
	invocation *Invocation,
	streams ProcessIO,
) error {
	if invocation == nil || len(invocation.Args) == 0 {
		return fmt.Errorf("background Bubblewrap invocation is empty")
	}
	if invocation.Mode != ExecutionNonInteractive {
		return fmt.Errorf(
			"background Bubblewrap execution mode must be %q",
			ExecutionNonInteractive,
		)
	}
	if streams.RegisterPrompter != nil {
		return fmt.Errorf(
			"background Bubblewrap must not register an approval prompter",
		)
	}
	if err := validateBackgroundStream("stdin", streams.Stdin); err != nil {
		return err
	}
	if err := validateBackgroundStream("stdout", streams.Stdout); err != nil {
		return err
	}
	if err := validateBackgroundStream("stderr", streams.Stderr); err != nil {
		return err
	}
	args, err := invocationArguments(invocation)
	if err != nil {
		return fmt.Errorf(
			"read background Bubblewrap arguments: %w",
			err,
		)
	}
	if err := validateBackgroundNamespacePrefix(args); err != nil {
		return err
	}
	for _, arg := range args[9:] {
		if arg == "--" {
			break
		}
		if arg == "--as-pid-1" {
			return fmt.Errorf(
				"background Bubblewrap requires its PID-namespace reaper",
			)
		}
	}

	return nil
}

func validateBackgroundStream(name string, stream any) error {
	if stream == nil {
		return nil
	}

	file, ok := stream.(*os.File)
	if !ok || file == nil {
		return fmt.Errorf(
			"background Bubblewrap %s must be unset or hold a non-nil direct *os.File",
			name,
		)
	}

	return nil
}

func validateBackgroundNamespacePrefix(args []string) error {
	if len(args) < 9 ||
		args[0] != "--unshare-user" ||
		args[1] != "--uid" ||
		args[3] != "--gid" ||
		args[5] != "--unshare-pid" ||
		args[6] != "--unshare-ipc" ||
		args[7] != "--unshare-uts" ||
		args[8] != "--die-with-parent" {
		return fmt.Errorf(
			"background Bubblewrap requires the fixed user, PID, IPC, UTS, and parent-death namespace policy",
		)
	}
	uid, uidErr := strconv.Atoi(args[2])
	gid, gidErr := strconv.Atoi(args[4])
	if uidErr != nil || uid < 0 || gidErr != nil || gid < 0 {
		return fmt.Errorf(
			"background Bubblewrap namespace UID and GID must be non-negative integers",
		)
	}

	return nil
}

func terminateStartedBackground(
	command *exec.Cmd,
	monitor *processIdentity,
	statusResult <-chan backgroundStatusResult,
	retained ...*processIdentity,
) error {
	var signalErr error
	for _, identity := range retained {
		if identity != nil {
			signalErr = errors.Join(
				signalErr,
				identity.Signal(syscall.SIGKILL),
			)
		}
	}
	if monitor != nil {
		signalErr = errors.Join(
			signalErr,
			monitor.Signal(syscall.SIGKILL),
		)
	} else if command != nil && command.Process != nil {
		signalErr = errors.Join(
			signalErr,
			command.Process.Kill(),
		)
	}

	waitErr := command.Wait()
	status := <-statusResult

	var identityErr error
	if monitor != nil {
		identityErr = monitor.Close()
	}
	for _, identity := range retained {
		if identity != nil {
			identityErr = errors.Join(
				identityErr,
				identity.WaitExited(),
				identity.Close(),
			)
		}
	}

	return errors.Join(signalErr, waitErr, status.err, identityErr)
}

func (p *backgroundProcess) wait(
	command *exec.Cmd,
	statusResult <-chan backgroundStatusResult,
) {
	waitErr := command.Wait()
	status := <-statusResult
	treeSignalErr := errors.Join(
		p.payload.Signal(syscall.SIGKILL),
		p.init.Signal(syscall.SIGKILL),
	)
	payloadWaitErr := p.payload.WaitExited()
	initWaitErr := p.init.WaitExited()

	p.mu.Lock()
	identityErr := errors.Join(
		p.monitor.Close(),
		p.init.Close(),
		p.payload.Close(),
	)
	p.monitor = nil
	p.init = nil
	p.payload = nil
	p.err = errors.Join(
		backgroundWaitError(waitErr),
		backgroundStatusError(status.status, status.err),
		treeSignalErr,
		payloadWaitErr,
		initWaitErr,
		identityErr,
	)
	close(p.done)
	p.mu.Unlock()
}

func backgroundWaitError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wait for background Bubblewrap: %w", err)
}

func backgroundStatusError(
	status bubblewrapStatus,
	err error,
) error {
	if err != nil {
		return err
	}
	if !status.hasChildPID {
		return fmt.Errorf(
			"background Bubblewrap exited before reporting a payload child",
		)
	}

	return nil
}

func (p *backgroundProcess) Done() <-chan struct{} {
	if p == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return p.done
}

func (p *backgroundProcess) Err() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *backgroundProcess) Stop(ctx context.Context) error {
	return p.signal(ctx, false)
}

func (p *backgroundProcess) Kill(ctx context.Context) error {
	return p.signal(ctx, true)
}

func (p *backgroundProcess) signal(
	ctx context.Context,
	force bool,
) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("signal background Bubblewrap: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.payload == nil {
		return nil
	}

	if !force {
		return p.payload.Signal(syscall.SIGTERM)
	}

	return errors.Join(
		p.payload.Signal(syscall.SIGKILL),
		p.monitor.Signal(syscall.SIGKILL),
	)
}
