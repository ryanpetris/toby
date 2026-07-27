//go:build linux

package bwrap

// Retains exact Linux process-group leaders for race-free foreground signal
// delivery and child-state observation.

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// PIDFD_SIGNAL_PROCESS_GROUP was added in Linux 6.9. Keep the value local
// until x/sys/unix exposes the UAPI constant.
const pidfdSignalProcessGroup = 1 << 2

type processGroupIdentity struct {
	process *processIdentity
}

func openProcessGroupIdentity(
	pid int,
	expectedParentPID int,
) (group *processGroupIdentity, returnErr error) {
	if expectedParentPID <= 0 {
		return nil, fmt.Errorf(
			"expected process-group parent PID must be positive",
		)
	}

	process, err := retainProcessIdentity(pid)
	if err != nil {
		return nil, err
	}
	group = &processGroupIdentity{process: process}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, group.Close())
			group = nil
		}
	}()

	parentPID, err := processParentPID(pid)
	if err != nil {
		return nil, err
	}
	if parentPID != expectedParentPID {
		return nil, fmt.Errorf(
			"process-group leader %d parent is %d, want %d",
			pid,
			parentPID,
			expectedParentPID,
		)
	}

	var processGroup int
	for {
		processGroup, err = unix.Getpgid(pid)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf(
			"inspect process group for leader %d: %w",
			pid,
			err,
		)
	}
	if processGroup != pid {
		return nil, fmt.Errorf(
			"process %d belongs to process group %d, want it to be the leader",
			pid,
			processGroup,
		)
	}

	return group, nil
}

func (p *processGroupIdentity) PID() int {
	if p == nil || p.process == nil {
		return 0
	}

	return p.process.pid
}

func (p *processGroupIdentity) Signal(signal syscall.Signal) error {
	if p == nil || p.process == nil || p.process.pidfd < 0 {
		return fmt.Errorf("process-group identity is closed")
	}

	var err error
	for {
		err = unix.PidfdSendSignal(
			p.process.pidfd,
			unix.Signal(signal),
			nil,
			pidfdSignalProcessGroup,
		)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf(
			"send %s to exact process group led by %d: %w",
			signal,
			p.process.pid,
			err,
		)
	}

	return nil
}

func (p *processGroupIdentity) Waitid(
	info *unix.Siginfo,
	options int,
) error {
	if p == nil || p.process == nil || p.process.pidfd < 0 {
		return fmt.Errorf("process-group identity is closed")
	}

	for {
		err := unix.Waitid(
			unix.P_PIDFD,
			p.process.pidfd,
			info,
			options,
			nil,
		)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func (p *processGroupIdentity) Close() error {
	if p == nil || p.process == nil {
		return nil
	}

	err := p.process.Close()
	p.process = nil
	return err
}
