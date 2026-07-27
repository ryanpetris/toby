//go:build linux

package bwrap

// Tracks the exact sandbox init reported by a foreground Bubblewrap monitor
// so a run does not reuse its writable overlay before namespace teardown.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/shutdown"
)

type foregroundStatusResult struct {
	status     bubblewrapStatus
	statusErr  error
	sandbox    *foregroundSandbox
	sandboxErr error
}

type foregroundSandbox struct {
	init           *processIdentity
	mntNamespaceFD *os.File
}

func trackForegroundStatus(
	reader io.Reader,
	monitorStarted <-chan int,
	gateWriter *os.File,
) <-chan foregroundStatusResult {
	result := make(chan foregroundStatusResult, 1)
	go func() {
		gateOpen := true
		var gateErr error
		releaseGate := func() {
			if !gateOpen {
				return
			}

			gateErr = gateWriter.Close()
			if gateErr != nil {
				gateErr = fmt.Errorf(
					"release Bubblewrap launch gate: %w",
					gateErr,
				)
			}
			gateOpen = false
		}

		var sandbox *foregroundSandbox
		var sandboxErr error
		status, statusErr := readBubblewrapStatusEvents(
			reader,
			func(child bubblewrapChildStatus) {
				monitorPID, started := <-monitorStarted
				if !started {
					sandboxErr = fmt.Errorf(
						"bubblewrap reported sandbox init before its monitor started",
					)
					releaseGate()
					return
				}

				sandbox, sandboxErr = retainForegroundSandbox(
					child,
					monitorPID,
				)
				releaseGate()
			},
		)
		releaseGate()
		result <- foregroundStatusResult{
			status:     status,
			statusErr:  statusErr,
			sandbox:    sandbox,
			sandboxErr: errors.Join(sandboxErr, gateErr),
		}
	}()

	return result
}

func retainForegroundSandbox(
	child bubblewrapChildStatus,
	expectedParentPID int,
) (sandbox *foregroundSandbox, returnErr error) {
	identity, err := retainProcessIdentity(child.pid)
	if errors.Is(err, unix.ESRCH) {
		// Setup can fail before Bubblewrap reaches --block-fd. JSON still
		// reports the short-lived child, but there is no surviving namespace
		// to retain and the structured status remains authoritative.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"retain exact foreground sandbox init: %w",
			err,
		)
	}
	sandbox = &foregroundSandbox{init: identity}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, sandbox.Close())
			sandbox = nil
		}
	}()

	exited, err := identity.Exited()
	if err != nil {
		return nil, err
	}
	if exited {
		return sandbox, nil
	}

	parentPID, err := processParentPID(child.pid)
	if err != nil {
		exited, exitErr := identity.Exited()
		if exitErr != nil {
			return nil, errors.Join(err, exitErr)
		}
		if exited {
			return sandbox, nil
		}
		return nil, err
	}
	if parentPID != expectedParentPID {
		return nil, fmt.Errorf(
			"foreground sandbox init %d parent is %d, want Bubblewrap monitor %d",
			child.pid,
			parentPID,
			expectedParentPID,
		)
	}

	namespacePath := "/proc/" + strconv.Itoa(child.pid) + "/ns/mnt"
	namespaceFD, err := unix.Open(
		namespacePath,
		unix.O_RDONLY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		exited, exitErr := identity.Exited()
		if exitErr != nil {
			return nil, errors.Join(err, exitErr)
		}
		if exited {
			return sandbox, nil
		}
		return nil, fmt.Errorf(
			"retain foreground sandbox mount namespace: %w",
			err,
		)
	}
	sandbox.mntNamespaceFD = os.NewFile(
		uintptr(namespaceFD),
		"foreground sandbox mount namespace",
	)

	var namespaceStat unix.Stat_t
	if err := unix.Fstat(namespaceFD, &namespaceStat); err != nil {
		return nil, fmt.Errorf(
			"inspect foreground sandbox mount namespace: %w",
			err,
		)
	}
	if uint64(namespaceStat.Ino) != child.mntNamespace {
		return nil, fmt.Errorf(
			"foreground sandbox mount namespace is %d, Bubblewrap reported %d",
			uint64(namespaceStat.Ino),
			child.mntNamespace,
		)
	}

	exited, err = identity.Exited()
	if err != nil {
		return nil, err
	}
	if exited {
		return sandbox, nil
	}

	return sandbox, nil
}

func finalizeForegroundSandbox(
	ctx context.Context,
	sandbox *foregroundSandbox,
	notify func(),
) (returnErr error) {
	if sandbox == nil {
		return nil
	}
	defer func() {
		returnErr = errors.Join(returnErr, sandbox.Close())
	}()

	exited, err := sandbox.init.Exited()
	if err != nil {
		return err
	}
	if exited {
		return nil
	}

	if notify != nil {
		notify()
	}
	wait := make(chan error, 1)
	go func() {
		wait <- sandbox.init.WaitExited()
	}()

	var waitErr error
	select {
	case waitErr = <-wait:
	case <-ctx.Done():
		timer := time.NewTimer(shutdown.SandboxReapGrace)
		defer timer.Stop()
		select {
		case waitErr = <-wait:
		case <-timer.C:
			return errors.Join(
				ctx.Err(),
				fmt.Errorf(
					"sandbox init did not exit within %s",
					shutdown.SandboxReapGrace,
				),
			)
		}
	}
	if waitErr != nil {
		return fmt.Errorf(
			"wait for foreground sandbox init exit: %w",
			waitErr,
		)
	}

	return nil
}

func (s *foregroundSandbox) Close() error {
	if s == nil {
		return nil
	}

	var namespaceErr error
	if s.mntNamespaceFD != nil {
		namespaceErr = s.mntNamespaceFD.Close()
		if namespaceErr != nil {
			namespaceErr = fmt.Errorf(
				"release foreground sandbox mount namespace: %w",
				namespaceErr,
			)
		}
		s.mntNamespaceFD = nil
	}

	identityErr := s.init.Close()
	s.init = nil

	return errors.Join(namespaceErr, identityErr)
}
