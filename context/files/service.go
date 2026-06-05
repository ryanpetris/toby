// Package contextfiles writes context files into the sandbox's context directory
// and tracks the instruction files among them so agents can be pointed at them.
package contextfiles

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/sandbox"
)

// Registrar is the narrow interface tool config packages use to contribute
// static context files during sandbox setup.
type Registrar interface {
	AddBytes(path string, data []byte, mode uint32) error
}

// File is a rendered context file: a sandbox-relative path, its bytes, and mode.
type File struct {
	Path string
	Data []byte
	Mode uint32
}

// Service writes context files into the sandbox and tracks instruction files so
// agents can be pointed at them.
type Service struct {
	mu                  sync.Mutex
	sandbox             sandbox.Service
	instructionPaths    []string
	instructionContents [][]byte
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) SetSandbox(sandbox sandbox.Service) {
	s.mu.Lock()
	s.sandbox = sandbox
	s.mu.Unlock()
}

func (s *Service) Reset() {
	s.mu.Lock()
	s.instructionPaths = nil
	s.instructionContents = nil
	s.mu.Unlock()
}

func (s *Service) Registrar(ctx context.Context) Registrar {
	return contextRegistrar{ctx: ctx, service: s}
}

func (s *Service) AddFile(ctx context.Context, path string, data []byte, mode uint32) (string, error) {
	return s.addFile(ctx, path, data, mode, false)
}

func (s *Service) AddInstruction(ctx context.Context, path string, data []byte, mode uint32) (string, error) {
	return s.addFile(ctx, path, data, mode, true)
}

func (s *Service) AddInstructionFS(ctx context.Context, path string, fsys fs.FS, name string, mode uint32) (string, error) {
	data, err := readFSFile(fsys, name)
	if err != nil {
		return "", err
	}
	return s.AddInstruction(ctx, path, data, mode)
}

type contextRegistrar struct {
	ctx     context.Context
	service *Service
}

func (r contextRegistrar) AddBytes(path string, data []byte, mode uint32) error {
	_, err := r.service.AddFile(r.ctx, path, data, mode)
	return err
}

func (s *Service) InstructionPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.instructionPaths...)
}

func (s *Service) InstructionContents() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	contents := make([][]byte, 0, len(s.instructionContents))
	for _, item := range s.instructionContents {
		contents = append(contents, append([]byte(nil), item...))
	}
	return contents
}

func (s *Service) addFile(ctx context.Context, path string, data []byte, mode uint32, instruction bool) (string, error) {
	path, err := cleanPath(path)
	if err != nil {
		return "", err
	}
	if mode == 0 {
		mode = 0o644
	}

	s.mu.Lock()
	sandbox := s.sandbox
	s.mu.Unlock()
	if sandbox == nil {
		return "", fmt.Errorf("sandbox service is not configured")
	}

	target := filepath.Join(layout.Context, filepath.FromSlash(path))
	if err := sandbox.AddFile(ctx, target, data, mode); err != nil {
		return "", err
	}

	if instruction {
		s.mu.Lock()
		s.instructionPaths = append(s.instructionPaths, target)
		s.instructionContents = append(s.instructionContents, append([]byte(nil), data...))
		s.mu.Unlock()
	}
	return target, nil
}

func cleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || !fs.ValidPath(path) {
		return "", fmt.Errorf("invalid context file path: %q", path)
	}
	return path, nil
}

func readFSFile(fsys fs.FS, name string) ([]byte, error) {
	if fsys == nil {
		return nil, fmt.Errorf("fs is required")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || !fs.ValidPath(name) {
		return nil, fmt.Errorf("invalid fs path")
	}
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("fs path is a directory: %s", name)
	}
	return fs.ReadFile(fsys, name)
}
