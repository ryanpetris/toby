//go:build linux

package git

// Retains exact pidfds and exhaustively terminates children adopted by the Git
// supervisor's Linux subreaper.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const gitDescendantRetryInterval = time.Millisecond

type gitProcessIdentity struct {
	pid   int
	pidfd int
}

func openGitProcessIdentity(
	pid int,
	expectedParentPID int,
) (identity *gitProcessIdentity, returnErr error) {
	if pid <= 0 {
		return nil, fmt.Errorf("git process identity PID must be positive")
	}
	if expectedParentPID <= 0 {
		return nil, fmt.Errorf(
			"expected Git process parent PID must be positive",
		)
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
		return nil, fmt.Errorf("open pidfd for Git process %d: %w", pid, err)
	}
	identity = &gitProcessIdentity{pid: pid, pidfd: pidfd}
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"Git process identity setup already failed",
				"close partial Git process identity",
				identity.Close(),
				"pid", pid,
			)
			identity = nil
		}
	}()

	parentPID, err := gitProcessParentPID(pid)
	if err != nil {
		return nil, err
	}
	if parentPID != expectedParentPID {
		return nil, fmt.Errorf(
			"git process %d parent is %d, want supervisor %d",
			pid,
			parentPID,
			expectedParentPID,
		)
	}

	return identity, nil
}

func (p *gitProcessIdentity) Signal(signal syscall.Signal) error {
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
			"send %s to exact Git process %d: %w",
			signal,
			p.pid,
			err,
		)
	}

	return nil
}

func (p *gitProcessIdentity) Close() error {
	if p == nil || p.pidfd < 0 {
		return nil
	}

	err := unix.Close(p.pidfd)
	p.pidfd = -1
	if err != nil {
		return fmt.Errorf("close pidfd for Git process %d: %w", p.pid, err)
	}

	return nil
}

func terminateAdoptedGitDescendants(supervisorPID int) error {
	var cleanupErr error
	remember := func(err error) {
		if err != nil && cleanupErr == nil {
			cleanupErr = err
		}
	}

	for {
		noChildren, err := reapExitedGitChildren()
		if err != nil {
			remember(err)
			time.Sleep(gitDescendantRetryInterval)
			continue
		}
		if noChildren {
			return cleanupErr
		}

		pids, err := directGitChildPIDs()
		if err != nil {
			remember(err)
			time.Sleep(gitDescendantRetryInterval)
			continue
		}
		if len(pids) == 0 {
			// wait4 proved that at least one live child remains. A task can
			// disappear while /proc/self/task is scanned, so retry until its
			// children are reparented to a surviving supervisor task.
			time.Sleep(gitDescendantRetryInterval)
			continue
		}

		identities := make([]*gitProcessIdentity, 0, len(pids))
		for _, pid := range pids {
			identity, err := openGitProcessIdentity(pid, supervisorPID)
			if err != nil {
				if !errors.Is(err, unix.ENOENT) &&
					!errors.Is(err, unix.ESRCH) {
					remember(err)
				}
				continue
			}
			identities = append(identities, identity)
		}
		if len(identities) == 0 {
			time.Sleep(gitDescendantRetryInterval)
			continue
		}

		signaled := identities[:0]
		for _, identity := range identities {
			if err := identity.Signal(syscall.SIGKILL); err != nil {
				remember(err)
				remember(identity.Close())
				continue
			}
			signaled = append(signaled, identity)
		}
		for _, identity := range signaled {
			remember(waitGitChild(identity.pid))
			remember(identity.Close())
		}
	}
}

func reapExitedGitChildren() (noChildren bool, returnErr error) {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf(
				"reap exited Git supervisor child: %w",
				err,
			)
		}
		if pid == 0 {
			return false, nil
		}
	}
}

func waitGitChild(pid int) error {
	for {
		var status unix.WaitStatus
		waited, err := unix.Wait4(pid, &status, 0, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf(
				"reap Git supervisor child %d: %w",
				pid,
				err,
			)
		}
		if waited != pid {
			return fmt.Errorf(
				"reaped Git supervisor child %d, want %d",
				waited,
				pid,
			)
		}

		return nil
	}
}

func directGitChildPIDs() ([]int, error) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, fmt.Errorf("list Git supervisor tasks: %w", err)
	}

	children := make(map[int]struct{})
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil || tid <= 0 {
			continue
		}

		data, err := os.ReadFile(filepath.Join(
			"/proc/self/task",
			strconv.Itoa(tid),
			"children",
		))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf(
				"read Git supervisor task %d children: %w",
				tid,
				err,
			)
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf(
					"git supervisor task %d has invalid child PID %q",
					tid,
					field,
				)
			}
			children[pid] = struct{}{}
		}
	}

	result := make([]int, 0, len(children))
	for pid := range children {
		result = append(result, pid)
	}
	sort.Ints(result)

	return result, nil
}

func gitProcessParentPID(pid int) (int, error) {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, fmt.Errorf("read Git process %d status: %w", pid, err)
	}

	for _, line := range strings.Split(string(status), "\n") {
		value, found := strings.CutPrefix(line, "PPid:")
		if !found {
			continue
		}

		parentPID, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parentPID <= 0 {
			return 0, fmt.Errorf(
				"git process %d has invalid parent PID %q",
				pid,
				strings.TrimSpace(value),
			)
		}

		return parentPID, nil
	}

	return 0, fmt.Errorf("git process %d status omits parent PID", pid)
}
