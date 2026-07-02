// File-lock helper: serializes the detect/bind/spawn dance across concurrent
// processes so exactly one daemon binds the socket. flock is advisory and
// process-scoped, which is exactly the granularity we want here.

package unixsocket

import (
	"os"
	"syscall"
)

// withLock opens (creating if needed) the lock file, takes an exclusive flock, runs
// fn, and releases the lock. Where flock is unsupported the open still succeeds and
// fn runs — the socket bind's own EADDRINUSE remains the backstop against a double
// bind.
func (s *Service) withLock(fn func() error) error {
	file, err := os.OpenFile(s.lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	return fn()
}
