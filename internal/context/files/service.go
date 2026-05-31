package contextfiles

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

type Registrar interface {
	AddBytes(path string, data []byte, mode uint32) error
	AddFS(path string, fsys fs.FS, name string, mode uint32) error
}

type FileSink interface {
	AddFile(path string, data []byte, mode uint32) error
}

type Service struct{}

type File struct {
	Path string
	Data []byte
	Mode uint32
}

type Builder struct {
	files []File
}

type Session struct {
	contextDir          string
	builder             *Builder
	sink                FileSink
	instructionPaths    []string
	instructionContents [][]byte
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) NewBuilder() *Builder {
	return &Builder{}
}

func (s *Service) NewSession(contextDir string) *Session {
	return &Session{contextDir: contextDir, builder: s.NewBuilder()}
}

func (s *Service) NewEmittingSession(contextDir string, sink FileSink) *Session {
	return &Session{contextDir: contextDir, sink: sink}
}

func (s *Service) Build(configure func(*Builder) error) ([]File, error) {
	builder := s.NewBuilder()
	if err := configure(builder); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}

func (s *Service) BuildSession(contextDir string, configure func(*Session) error) ([]File, error) {
	session := s.NewSession(contextDir)
	if err := configure(session); err != nil {
		return nil, err
	}
	return session.Files(), nil
}

func (s *Session) ContextDir() string {
	if s == nil {
		return ""
	}
	return s.contextDir
}

func (s *Session) AddBytes(path string, data []byte, mode uint32) error {
	if s == nil || (s.builder == nil && s.sink == nil) {
		return fmt.Errorf("context files session is not configured")
	}
	path, err := cleanPath(path)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o400
	}
	if s.builder != nil {
		if err := s.builder.addCleanBytes(path, data, mode); err != nil {
			return err
		}
	}
	if s.sink != nil {
		return s.sink.AddFile(path, data, mode)
	}
	return nil
}

func (s *Session) AddFS(path string, fsys fs.FS, name string, mode uint32) error {
	if s == nil || s.builder == nil {
		return fmt.Errorf("context files session is not configured")
	}
	return s.builder.AddFS(path, fsys, name, mode)
}

func (s *Session) AddInstructionBytes(path string, data []byte, mode uint32) error {
	if err := s.AddBytes(path, data, mode); err != nil {
		return err
	}
	clean, err := cleanPath(path)
	if err != nil {
		return err
	}
	s.instructionPaths = append(s.instructionPaths, filepath.Join(s.contextDir, filepath.FromSlash(clean)))
	s.instructionContents = append(s.instructionContents, append([]byte(nil), data...))
	return nil
}

func (s *Session) AddInstructionFS(path string, fsys fs.FS, name string, mode uint32) error {
	if fsys == nil {
		return fmt.Errorf("fs is required")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || !fs.ValidPath(name) {
		return fmt.Errorf("invalid fs path")
	}
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("fs path is a directory: %s", name)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	return s.AddInstructionBytes(path, data, mode)
}

func (s *Session) InstructionPaths() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.instructionPaths...)
}

func (s *Session) InstructionContents() [][]byte {
	if s == nil {
		return nil
	}
	contents := make([][]byte, 0, len(s.instructionContents))
	for _, item := range s.instructionContents {
		contents = append(contents, append([]byte(nil), item...))
	}
	return contents
}

func (s *Session) Files() []File {
	if s == nil || s.builder == nil {
		return nil
	}
	return s.builder.Files()
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.instructionPaths = nil
	s.instructionContents = nil
	if s.builder == nil {
		return nil
	}
	return s.builder.Close()
}

func (b *Builder) AddBytes(path string, data []byte, mode uint32) error {
	path, err := cleanPath(path)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o400
	}
	return b.addCleanBytes(path, data, mode)
}

func (b *Builder) addCleanBytes(path string, data []byte, mode uint32) error {
	b.files = append(b.files, File{Path: path, Data: append([]byte(nil), data...), Mode: mode})
	return nil
}

func (b *Builder) AddFS(path string, fsys fs.FS, name string, mode uint32) error {
	if fsys == nil {
		return fmt.Errorf("fs is required")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || !fs.ValidPath(name) {
		return fmt.Errorf("invalid fs path")
	}
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("fs path is a directory: %s", name)
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	return b.AddBytes(path, data, mode)
}

func (b *Builder) Files() []File {
	files := make([]File, 0, len(b.files))
	for _, file := range b.files {
		files = append(files, File{Path: file.Path, Data: append([]byte(nil), file.Data...), Mode: file.Mode})
	}
	return files
}

func (b *Builder) Close() error {
	b.files = nil
	return nil
}

func cleanPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || !fs.ValidPath(path) {
		return "", fmt.Errorf("invalid context file path: %q", path)
	}
	return path, nil
}
