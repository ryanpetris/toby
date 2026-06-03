package mcpproxy

import (
	"context"
	"fmt"
	"time"
)

func (s *Service) start(ctx context.Context, entry *Entry) {
	if entry == nil {
		return
	}
	s.mu.Lock()
	if entry.Status == StatusStarting || entry.Status == StatusRunning {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel
	if s.runner != nil {
		entry.Spec = s.runner.PrepareStart(entry.Spec)
	}
	entry.Status = StatusStarting
	entry.LastError = ""
	entry.UpdatedAt = time.Now()
	s.mu.Unlock()

	handle, err := s.runner.Start(runCtx, entry.Spec)
	if err != nil {
		cancel()
		s.setFailed(entry, err)
		return
	}
	s.mu.Lock()
	entry.handle = handle
	entry.Status = StatusRunning
	entry.UpdatedAt = time.Now()
	s.mu.Unlock()

	if entry.Bridge != nil {
		go func() {
			if err := entry.Bridge.Attach(runCtx, handle); err != nil {
				s.setFailed(entry, err)
			}
		}()
	}

	result := <-handle.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Status == StatusStopped {
		return
	}
	entry.ExitCode = result.ExitCode
	entry.UpdatedAt = time.Now()
	if result.Err != nil && runCtx.Err() == nil {
		entry.Status = StatusFailed
		entry.LastError = result.Err.Error()
		return
	}
	entry.Status = StatusExited
}

func (s *Service) stop(ctx context.Context, entry *Entry) error {
	if entry == nil {
		return nil
	}
	s.mu.Lock()
	cancel := entry.cancel
	handle := entry.handle
	entry.Status = StatusStopped
	entry.UpdatedAt = time.Now()
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if handle != nil {
		return handle.Stop(ctx)
	}
	return nil
}

func (s *Service) setFailed(entry *Entry, err error) {
	if entry == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Status = StatusFailed
	entry.LastError = err.Error()
	entry.UpdatedAt = time.Now()
}

func (s *Service) Start(ctx context.Context, name string) error {
	entry, ok := s.entry(name)
	if !ok {
		return fmt.Errorf("mcp %q is not configured", name)
	}
	if entry.Remote {
		return fmt.Errorf("mcp %q is remote", name)
	}
	go s.start(ctx, entry)
	return nil
}

func (s *Service) Stop(ctx context.Context, name string) error {
	entry, ok := s.entry(name)
	if !ok {
		return fmt.Errorf("mcp %q is not configured", name)
	}
	if entry.Remote {
		return fmt.Errorf("mcp %q is remote", name)
	}
	return s.stop(ctx, entry)
}

func (s *Service) Restart(ctx context.Context, name string) error {
	if err := s.Stop(ctx, name); err != nil {
		return err
	}
	return s.Start(ctx, name)
}

func (s *Service) entry(name string) (*Entry, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	entry, ok := s.entries[name]
	s.mu.RUnlock()
	return entry, ok
}
