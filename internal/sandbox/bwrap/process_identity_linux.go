//go:build linux

package bwrap

// Retains and operates on exact Linux process identities so delayed probe
// cleanup cannot affect an unrelated process after numeric PID reuse.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

type processIdentity struct {
	pid   int
	pidfd int
}

func openProcessIdentity(
	pid int,
	expectedParentPID int,
) (identity *processIdentity, returnErr error) {
	if pid <= 0 {
		return nil, fmt.Errorf("process identity PID must be positive")
	}
	if expectedParentPID <= 0 {
		return nil, fmt.Errorf("expected process parent PID must be positive")
	}

	identity, err := retainProcessIdentity(pid)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, identity.Close())
			identity = nil
		}
	}()

	exited, err := identity.Exited()
	if err != nil {
		return nil, err
	}
	if exited {
		return nil, fmt.Errorf(
			"process %d exited before its identity was validated",
			pid,
		)
	}

	parentPID, err := processParentPID(pid)
	if err != nil {
		return nil, err
	}
	if parentPID != expectedParentPID {
		return nil, fmt.Errorf(
			"process %d parent is %d, want wrapper %d",
			pid,
			parentPID,
			expectedParentPID,
		)
	}

	exited, err = identity.Exited()
	if err != nil {
		return nil, err
	}
	if exited {
		return nil, fmt.Errorf(
			"process %d exited while its identity was validated",
			pid,
		)
	}

	return identity, nil
}

func retainProcessIdentity(pid int) (*processIdentity, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("process identity PID must be positive")
	}

	var pidfd int
	var err error
	for {
		pidfd, err = unix.PidfdOpen(pid, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open pidfd for process %d: %w", pid, err)
	}

	return &processIdentity{pid: pid, pidfd: pidfd}, nil
}

func (p *processIdentity) Exited() (bool, error) {
	if p == nil || p.pidfd < 0 {
		return false, fmt.Errorf("process identity is closed")
	}

	poll := []unix.PollFd{{
		Fd:     int32(p.pidfd),
		Events: unix.POLLIN,
	}}
	for {
		count, err := unix.Poll(poll, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf(
				"poll pidfd for process %d: %w",
				p.pid,
				err,
			)
		}
		if count == 0 {
			return false, nil
		}
		if poll[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return true, nil
		}

		return false, fmt.Errorf(
			"poll pidfd for process %d returned events %#x",
			p.pid,
			poll[0].Revents,
		)
	}
}

func (p *processIdentity) Signal(signal syscall.Signal) error {
	if p == nil || p.pidfd < 0 {
		return nil
	}

	err := unix.PidfdSendSignal(
		p.pidfd,
		unix.Signal(signal),
		nil,
		0,
	)
	if err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf(
			"send %s to exact process %d: %w",
			signal,
			p.pid,
			err,
		)
	}

	return nil
}

func (p *processIdentity) WaitExited() error {
	if p == nil || p.pidfd < 0 {
		return fmt.Errorf("process identity is closed")
	}

	poll := []unix.PollFd{{
		Fd:     int32(p.pidfd),
		Events: unix.POLLIN,
	}}
	for {
		count, err := unix.Poll(poll, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf(
				"wait on pidfd for process %d: %w",
				p.pid,
				err,
			)
		}
		if count == 1 &&
			poll[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			return nil
		}

		return fmt.Errorf(
			"wait on pidfd for process %d returned events %#x",
			p.pid,
			poll[0].Revents,
		)
	}
}

func (p *processIdentity) Close() error {
	if p == nil || p.pidfd < 0 {
		return nil
	}

	err := unix.Close(p.pidfd)
	p.pidfd = -1
	diagnostic.DiscardError(
		"releasing a retained process identity is cleanup",
		"close process identity",
		err,
		"pid", p.pid,
	)
	return nil
}

func processParentPID(pid int) (int, error) {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, fmt.Errorf("read process %d status: %w", pid, err)
	}

	for _, line := range strings.Split(string(status), "\n") {
		value, found := strings.CutPrefix(line, "PPid:")
		if !found {
			continue
		}

		parentPID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parentPID <= 0 {
			return 0, fmt.Errorf(
				"process %d has invalid parent PID %q",
				pid,
				strings.TrimSpace(value),
			)
		}

		return parentPID, nil
	}

	return 0, fmt.Errorf("process %d status omits parent PID", pid)
}
