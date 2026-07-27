//go:build linux

package run

// Exposes the attached Bubblewrap run to package-local integration tests.

import "petris.dev/toby/internal/sandbox/bwrap"

func (r *NativeRun) bubblewrapRun() *bwrap.Run {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	return r.bubblewrap
}
