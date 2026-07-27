//go:build linux

package bwrap

// Carries an exact payload pidfd from the trusted exec shim to the launch
// client and queues foreground signals until that identity is available.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
)

const payloadSignalByte = byte(1)

type payloadSignalRelay struct {
	register func(func(syscall.Signal) error) func()
	target   *payloadSignalTarget

	reader  *os.File
	writer  *os.File
	done    chan struct{}
	err     error
	started bool
}

func newPayloadSignalRelay(
	register func(func(syscall.Signal) error) func(),
	target *payloadSignalTarget,
) (*payloadSignalRelay, error) {
	if register == nil {
		return nil, nil
	}
	if target == nil {
		return nil, fmt.Errorf("payload signal target is nil")
	}

	descriptors, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("create payload-signal socket pair: %w", err)
	}

	reader := os.NewFile(
		uintptr(descriptors[0]),
		"payload-signal receiver",
	)
	writer := os.NewFile(
		uintptr(descriptors[1]),
		"payload-signal sender",
	)
	if reader == nil || writer == nil {
		if reader != nil {
			diagnostic.DiscardError(
				"payload-signal socket construction failed",
				"close payload-signal receiver",
				reader.Close(),
			)
		} else {
			diagnostic.DiscardError(
				"payload-signal socket construction failed",
				"close payload-signal receiver descriptor",
				unix.Close(descriptors[0]),
			)
		}
		if writer != nil {
			diagnostic.DiscardError(
				"payload-signal socket construction failed",
				"close payload-signal sender",
				writer.Close(),
			)
		} else {
			diagnostic.DiscardError(
				"payload-signal socket construction failed",
				"close payload-signal sender descriptor",
				unix.Close(descriptors[1]),
			)
		}
		return nil, fmt.Errorf(
			"create payload-signal socket files",
		)
	}

	relay := &payloadSignalRelay{
		register: register,
		target:   target,
		reader:   reader,
		writer:   writer,
		done:     make(chan struct{}),
	}
	go relay.watch()

	return relay, nil
}

func (r *payloadSignalRelay) registerHandler(
	_ func(syscall.Signal) error,
) func() {
	if r == nil || r.register == nil || r.target == nil {
		return func() {}
	}

	return r.register(r.target.Signal)
}

func (r *payloadSignalRelay) prepare(invocation *Invocation) error {
	if r == nil || r.writer == nil {
		return fmt.Errorf("payload-signal relay is not initialized")
	}
	if invocation == nil {
		return fmt.Errorf("payload-signal invocation is nil")
	}
	if invocation.payloadArgIndex <= 0 ||
		invocation.payloadArgIndex >= len(invocation.Args) {
		return fmt.Errorf("payload-signal invocation has no payload boundary")
	}

	signalFD := childExtraFileBaseFD + len(invocation.ExtraFiles)
	index := invocation.payloadArgIndex
	if isPayloadDispatch(invocation.Args, index) {
		invocation.Args[index+4] = strconv.Itoa(signalFD)
	} else {
		payload := append([]string(nil), invocation.Args[index:]...)
		arguments := append([]string(nil), invocation.Args[:index]...)
		arguments = append(
			arguments,
			layout.SandboxBinary(),
			"exec",
			"-1",
			"-1",
			strconv.Itoa(signalFD),
			"--",
		)
		invocation.Args = append(arguments, payload...)
	}

	invocation.ExtraFiles = append(invocation.ExtraFiles, r.writer)
	r.writer = nil

	return nil
}

func isPayloadDispatch(arguments []string, index int) bool {
	return index > 0 &&
		index+5 < len(arguments) &&
		arguments[index] == layout.SandboxBinary() &&
		arguments[index+1] == "exec" &&
		arguments[index+4] == "-1" &&
		arguments[index+5] == "--"
}

func (r *payloadSignalRelay) close() error {
	if r == nil {
		return nil
	}
	if r.writer != nil {
		diagnostic.DiscardError(
			"closing an unused payload-signal sender completes its watcher",
			"close payload-signal sender",
			r.writer.Close(),
		)
		r.writer = nil
	}

	<-r.done

	return r.err
}

func (r *payloadSignalRelay) payloadStarted() bool {
	if r == nil {
		return false
	}

	<-r.done
	return r.started
}

func (r *payloadSignalRelay) watch() {
	defer close(r.done)
	defer func() {
		diagnostic.DiscardError(
			"closing the consumed payload-signal receiver is cleanup",
			"close payload-signal receiver",
			r.reader.Close(),
		)
		r.reader = nil
	}()

	pidfd, received, err := receivePayloadPIDFD(r.reader)
	if err != nil {
		r.err = err
		return
	}
	if !received {
		return
	}
	r.started = true
	if err := r.target.Attach(pidfd); err != nil {
		r.err = err
	}
}

func receivePayloadPIDFD(file *os.File) (int, bool, error) {
	if file == nil {
		return -1, false, fmt.Errorf(
			"payload-signal receiver is nil",
		)
	}

	var payload [1]byte
	control := make([]byte, unix.CmsgSpace(4))
	var count int
	var controlCount int
	var flags int
	var err error
	for {
		count, controlCount, flags, _, err = unix.Recvmsg(
			int(file.Fd()),
			payload[:],
			control,
			unix.MSG_CMSG_CLOEXEC,
		)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return -1, false, fmt.Errorf(
			"receive payload process identity: %w",
			err,
		)
	}
	if count == 0 && controlCount == 0 {
		return -1, false, nil
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return -1, false, fmt.Errorf(
			"payload process identity was truncated",
		)
	}
	if count != 1 || payload[0] != payloadSignalByte {
		return -1, false, fmt.Errorf(
			"payload process marker is invalid",
		)
	}

	messages, err := unix.ParseSocketControlMessage(control[:controlCount])
	if err != nil {
		return -1, false, fmt.Errorf(
			"parse payload process identity: %w",
			err,
		)
	}
	var descriptors []int
	for _, message := range messages {
		rights, rightsErr := unix.ParseUnixRights(&message)
		if rightsErr != nil {
			closePayloadDescriptors(descriptors)
			return -1, false, fmt.Errorf(
				"parse payload process descriptor: %w",
				rightsErr,
			)
		}
		descriptors = append(descriptors, rights...)
	}
	if len(descriptors) != 1 {
		closePayloadDescriptors(descriptors)
		return -1, false, fmt.Errorf(
			"payload process identity has %d descriptors, want one",
			len(descriptors),
		)
	}

	return descriptors[0], true, nil
}

func closePayloadDescriptors(descriptors []int) {
	for _, descriptor := range descriptors {
		diagnostic.DiscardError(
			"discarding a malformed payload process identity is cleanup",
			"close payload process descriptor",
			unix.Close(descriptor),
		)
	}
}

type payloadSignalTarget struct {
	mu sync.Mutex

	pidfd   int
	pending []syscall.Signal
	closed  bool
}

func newPayloadSignalTarget() *payloadSignalTarget {
	return &payloadSignalTarget{pidfd: -1}
}

func (t *payloadSignalTarget) Signal(signal syscall.Signal) error {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	if t.pidfd < 0 {
		for _, current := range t.pending {
			if current == signal {
				return nil
			}
		}
		t.pending = append(t.pending, signal)
		return nil
	}

	return signalPayloadPIDFD(t.pidfd, signal)
}

func (t *payloadSignalTarget) Attach(pidfd int) error {
	if t == nil {
		diagnostic.DiscardError(
			"the payload signal target is unavailable",
			"close unclaimed payload process descriptor",
			unix.Close(pidfd),
		)
		return nil
	}
	if pidfd < 0 {
		return fmt.Errorf("payload process descriptor is invalid")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		diagnostic.DiscardError(
			"the payload signal target is already closed",
			"close late payload process descriptor",
			unix.Close(pidfd),
		)
		return nil
	}
	if t.pidfd >= 0 {
		diagnostic.DiscardError(
			"the payload signal target already has an identity",
			"close duplicate payload process descriptor",
			unix.Close(pidfd),
		)
		return fmt.Errorf("payload process identity was attached twice")
	}

	t.pidfd = pidfd
	var signalErr error
	for _, signal := range t.pending {
		signalErr = errors.Join(
			signalErr,
			signalPayloadPIDFD(t.pidfd, signal),
		)
	}
	t.pending = nil

	return signalErr
}

func (t *payloadSignalTarget) Close() {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.closed = true
	t.pending = nil
	if t.pidfd < 0 {
		return
	}

	diagnostic.DiscardError(
		"releasing the retained payload process identity is cleanup",
		"close payload process descriptor",
		unix.Close(t.pidfd),
	)
	t.pidfd = -1
}

func signalPayloadPIDFD(pidfd int, signal syscall.Signal) error {
	var err error
	for {
		err = unix.PidfdSendSignal(
			pidfd,
			unix.Signal(signal),
			nil,
			0,
		)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf(
			"send %s to exact sandbox payload: %w",
			signal,
			err,
		)
	}

	return nil
}
