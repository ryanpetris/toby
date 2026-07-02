// Registry: the race-safe project table. Acquire brings a project up (once, even
// under concurrent first invocations) and adds a session; Release drops it and arms
// the idle timer; the idle timer or an explicit stop tears the container down. Every
// decision is made under mu at a commit point; the channels only wait.

package project

import (
	"context"
	"sync"
	"time"

	"petris.dev/toby/internal/daemon/protocol"
)

// Registry owns the per-project entries.
type Registry struct {
	mu          sync.Mutex
	entries     map[Key]*entry
	lifecycle   Lifecycle
	idleTimeout time.Duration
	onEmpty     func()
}

// NewRegistry builds a registry. idleTimeout is how long a project stays warm after
// its last session exits; onEmpty (optional) fires when the last project is gone.
func NewRegistry(lifecycle Lifecycle, idleTimeout time.Duration, onEmpty func()) *Registry {
	return &Registry{
		entries:     map[Key]*entry{},
		lifecycle:   lifecycle,
		idleTimeout: idleTimeout,
		onEmpty:     onEmpty,
	}
}

// Acquire ensures the project for key is up and adds a session, returning a Session
// whose Release drops it. Concurrent Acquires for the same key funnel through the
// entry's ready channel so only one BringUp runs; an Acquire that arrives during
// teardown waits for it to finish and then brings the project up fresh.
func (r *Registry) Acquire(ctx context.Context, key Key, req Request) (*Session, error) {
	for {
		r.mu.Lock()
		e, ok := r.entries[key]
		if !ok {
			session, err, retry := r.startAndAcquire(ctx, key, req)
			if !retry {
				return session, err
			}
			continue
		}

		switch e.state {
		case stateReady:
			r.acquireLocked(e)
			r.mu.Unlock()
			return &Session{registry: r, entry: e}, nil
		case stateStarting:
			ready := e.ready
			r.mu.Unlock()
			<-ready
			if err := r.bringUpError(e); err != nil {
				return nil, err
			}
			// State is now Ready or already tearing down; loop to re-evaluate.
			continue
		default: // stateDraining or stateGone
			done := e.done
			r.mu.Unlock()
			if done != nil {
				<-done
			}
			// The entry is (or will be) removed; loop to create a fresh one.
			continue
		}
	}
}

// startAndAcquire creates a fresh Starting entry, runs BringUp outside the lock, and
// on success acquires a session. It is called with r.mu held and returns with r.mu
// released. retry is true when the caller should loop (a concurrent racer won the
// slot); in that case session/err are ignored.
func (r *Registry) startAndAcquire(ctx context.Context, key Key, req Request) (session *Session, err error, retry bool) {
	e := &entry{key: key, state: stateStarting, ready: make(chan struct{})}
	r.entries[key] = e
	r.mu.Unlock()

	handle, bringErr := r.lifecycle.BringUp(ctx, key, req)

	r.mu.Lock()
	defer r.mu.Unlock()
	if bringErr != nil {
		e.err = bringErr
		e.state = stateGone
		delete(r.entries, key)
		close(e.ready)
		return nil, bringErr, false
	}
	e.handle = handle
	e.state = stateReady
	close(e.ready)
	r.acquireLocked(e)
	return &Session{registry: r, entry: e}, nil, false
}

func (r *Registry) bringUpError(e *entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return e.err
}

// acquireLocked adds a session and disarms any pending idle teardown. Caller holds mu.
func (r *Registry) acquireLocked(e *entry) {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
	e.sessions++
}

// release drops a session and, when the project falls idle, arms the idle timer.
func (r *Registry) release(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.sessions > 0 {
		e.sessions--
	}
	if e.sessions == 0 && e.state == stateReady && r.idleTimeout > 0 {
		e.idleTimer = time.AfterFunc(r.idleTimeout, func() { r.idleTeardown(e) })
	}
}

// idleTeardown tears an idle project down. It re-checks under the lock that the
// project is still idle and Ready, so an Acquire that slipped in cancels it.
func (r *Registry) idleTeardown(e *entry) {
	r.mu.Lock()
	if e.state != stateReady || e.sessions > 0 {
		r.mu.Unlock()
		return
	}
	r.beginTeardownLocked(e)
	handle := e.handle
	r.mu.Unlock()

	r.finishTeardown(e, handle)
}

// beginTeardownLocked moves an entry into Draining. Caller holds mu.
func (r *Registry) beginTeardownLocked(e *entry) {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
	e.state = stateDraining
	e.done = make(chan struct{})
}

// finishTeardown runs the heavy TearDown outside the lock, then marks the entry Gone,
// removes it, and fires onEmpty when the registry is empty.
func (r *Registry) finishTeardown(e *entry, handle Handle) {
	if handle != nil {
		r.lifecycle.TearDown(handle)
	}

	r.mu.Lock()
	e.state = stateGone
	delete(r.entries, e.key)
	close(e.done)
	empty := len(r.entries) == 0
	onEmpty := r.onEmpty
	r.mu.Unlock()

	if empty && onEmpty != nil {
		onEmpty()
	}
}

// Stop tears down one project immediately (an explicit `toby stop <env>`), regardless
// of active sessions — those sessions' container is yanked, which is the point of an
// explicit stop. A project mid-bring-up is left to finish; callers can retry.
func (r *Registry) Stop(key Key) {
	r.mu.Lock()
	e, ok := r.entries[key]
	if !ok || e.state != stateReady {
		r.mu.Unlock()
		return
	}
	r.beginTeardownLocked(e)
	handle := e.handle
	r.mu.Unlock()

	r.finishTeardown(e, handle)
}

// StopByLabel tears down every Ready project whose environment label matches, and
// reports how many were stopped. Used by `toby stop <env>`.
func (r *Registry) StopByLabel(label string) int {
	r.mu.Lock()
	type pending struct {
		e      *entry
		handle Handle
	}
	var todo []pending
	for _, e := range r.entries {
		if e.key.Label == label && e.state == stateReady {
			r.beginTeardownLocked(e)
			todo = append(todo, pending{e: e, handle: e.handle})
		}
	}
	r.mu.Unlock()

	for _, p := range todo {
		r.finishTeardown(p.e, p.handle)
	}
	return len(todo)
}

// Shutdown tears down every Ready project (daemon stop).
func (r *Registry) Shutdown() {
	r.mu.Lock()
	type pending struct {
		e      *entry
		handle Handle
	}
	var todo []pending
	for _, e := range r.entries {
		if e.state == stateReady {
			r.beginTeardownLocked(e)
			todo = append(todo, pending{e: e, handle: e.handle})
		}
	}
	r.mu.Unlock()

	for _, p := range todo {
		r.finishTeardown(p.e, p.handle)
	}
}

// StatusList reports the sanitized state of every live project for daemon.status.
func (r *Registry) StatusList() []protocol.ProjectStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.ProjectStatus, 0, len(r.entries))
	for _, e := range r.entries {
		containerID := ""
		if e.handle != nil {
			containerID = e.handle.ContainerID()
		}
		out = append(out, protocol.ProjectStatus{
			Label:       e.key.Label,
			ContainerID: containerID,
			Sessions:    e.sessions,
		})
	}
	return out
}
