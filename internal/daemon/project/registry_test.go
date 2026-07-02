package project

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHandle struct{ id string }

func (h *fakeHandle) ContainerID() string { return h.id }

// fakeLifecycle records bring-up/teardown counts and can be told to delay, fail, or
// block teardown on a gate so races can be driven deterministically.
type fakeLifecycle struct {
	mu        sync.Mutex
	brings    int32
	tears     int32
	bringWait time.Duration
	failNext  bool
	tearGate  chan struct{} // when non-nil, TearDown blocks until it is closed
	tornDown  chan struct{} // closed once per TearDown entry
}

func (f *fakeLifecycle) BringUp(_ context.Context, key Key, _ Request) (Handle, error) {
	atomic.AddInt32(&f.brings, 1)
	f.mu.Lock()
	wait, fail := f.bringWait, f.failNext
	f.failNext = false
	f.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
	if fail {
		return nil, errors.New("bring-up failed")
	}
	return &fakeHandle{id: "ctr-" + key.Digest[:6]}, nil
}

func (f *fakeLifecycle) TearDown(Handle) {
	f.mu.Lock()
	gate, done := f.tearGate, f.tornDown
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	atomic.AddInt32(&f.tears, 1)
	if done != nil {
		done <- struct{}{}
	}
}

func TestConcurrentAcquireBringsUpOnce(t *testing.T) {
	life := &fakeLifecycle{bringWait: 20 * time.Millisecond}
	reg := NewRegistry(life, time.Minute, nil)
	key := NewKey("proj", "default", []string{"/a"})

	const n = 8
	var wg sync.WaitGroup
	sessions := make([]*Session, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := reg.Acquire(context.Background(), key, nil)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			sessions[i] = s
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&life.brings); got != 1 {
		t.Fatalf("brings = %d, want 1", got)
	}
	for i, s := range sessions {
		if s == nil {
			t.Fatalf("session %d nil", i)
		}
	}
	status := reg.StatusList()
	if len(status) != 1 || status[0].Sessions != n {
		t.Fatalf("status = %+v, want 1 project with %d sessions", status, n)
	}
}

func TestDifferentKeysBringUpSeparately(t *testing.T) {
	life := &fakeLifecycle{}
	reg := NewRegistry(life, time.Minute, nil)
	if _, err := reg.Acquire(context.Background(), NewKey("a", "default", []string{"/a"}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Acquire(context.Background(), NewKey("b", "default", []string{"/b"}), nil); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&life.brings); got != 2 {
		t.Fatalf("brings = %d, want 2", got)
	}
}

func TestIdleTeardownAfterLastRelease(t *testing.T) {
	life := &fakeLifecycle{tornDown: make(chan struct{}, 1)}
	reg := NewRegistry(life, 15*time.Millisecond, nil)
	key := NewKey("proj", "default", []string{"/a"})

	s, err := reg.Acquire(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()

	select {
	case <-life.tornDown:
	case <-time.After(time.Second):
		t.Fatal("idle teardown did not fire")
	}
	if got := atomic.LoadInt32(&life.tears); got != 1 {
		t.Fatalf("tears = %d, want 1", got)
	}
	if len(reg.StatusList()) != 0 {
		t.Fatal("project still listed after teardown")
	}
}

func TestAcquireCancelsIdleTeardown(t *testing.T) {
	life := &fakeLifecycle{}
	reg := NewRegistry(life, 40*time.Millisecond, nil)
	key := NewKey("proj", "default", []string{"/a"})

	s1, err := reg.Acquire(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	s1.Release() // arms the idle timer

	// Re-acquire before the timer fires; this must cancel the pending teardown.
	s2, err := reg.Acquire(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)

	if got := atomic.LoadInt32(&life.tears); got != 0 {
		t.Fatalf("tears = %d, want 0 (teardown should have been cancelled)", got)
	}
	if got := atomic.LoadInt32(&life.brings); got != 1 {
		t.Fatalf("brings = %d, want 1 (warm container reused)", got)
	}
	s2.Release()
}

func TestAcquireDuringDrainingRecreates(t *testing.T) {
	gate := make(chan struct{})
	life := &fakeLifecycle{tearGate: gate}
	reg := NewRegistry(life, time.Minute, nil)
	key := NewKey("proj", "default", []string{"/a"})

	s1, err := reg.Acquire(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	s1.Release()

	// Force teardown; TearDown blocks on the gate, holding the entry in Draining.
	go reg.Stop(key)
	// Give Stop a moment to enter Draining.
	time.Sleep(20 * time.Millisecond)

	// Acquire now: it must wait for the in-flight teardown, then bring up fresh.
	acquired := make(chan *Session, 1)
	go func() {
		s, err := reg.Acquire(context.Background(), key, nil)
		if err != nil {
			t.Errorf("acquire during draining: %v", err)
		}
		acquired <- s
	}()

	// The acquire is blocked until teardown completes; release the gate.
	time.Sleep(20 * time.Millisecond)
	close(gate)

	select {
	case s := <-acquired:
		if s == nil {
			t.Fatal("nil session after recreate")
		}
	case <-time.After(time.Second):
		t.Fatal("acquire during draining never completed")
	}
	if got := atomic.LoadInt32(&life.brings); got != 2 {
		t.Fatalf("brings = %d, want 2 (recreated after teardown)", got)
	}
	if got := atomic.LoadInt32(&life.tears); got != 1 {
		t.Fatalf("tears = %d, want 1", got)
	}
}

func TestBringUpErrorPropagatesToWaiters(t *testing.T) {
	life := &fakeLifecycle{bringWait: 20 * time.Millisecond, failNext: true}
	reg := NewRegistry(life, time.Minute, nil)
	key := NewKey("proj", "default", []string{"/a"})

	// Two concurrent acquires; the first runs BringUp (fails), the second waits on
	// ready and must observe the same failure rather than hang.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = reg.Acquire(context.Background(), key, nil)
		}(i)
	}
	wg.Wait()

	failures := 0
	for _, err := range errs {
		if err != nil {
			failures++
		}
	}
	if failures == 0 {
		t.Fatal("expected at least one acquire to see the bring-up error")
	}
	if len(reg.StatusList()) != 0 {
		t.Fatal("failed project should not remain listed")
	}
}

func TestOnEmptyFiresWhenLastProjectGone(t *testing.T) {
	var emptied int32
	life := &fakeLifecycle{tornDown: make(chan struct{}, 1)}
	reg := NewRegistry(life, 10*time.Millisecond, func() { atomic.AddInt32(&emptied, 1) })

	s, err := reg.Acquire(context.Background(), NewKey("a", "default", []string{"/a"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Release()

	select {
	case <-life.tornDown:
	case <-time.After(time.Second):
		t.Fatal("teardown did not fire")
	}
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&emptied) != 1 {
		t.Fatalf("onEmpty fired %d times, want 1", emptied)
	}
}
