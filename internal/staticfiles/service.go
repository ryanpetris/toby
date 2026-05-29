package staticfiles

import (
	"fmt"
	"io/fs"
	"strings"
)

type Registrar interface {
	AddBytes(path string, data []byte, mode uint32) error
	AddFS(path string, fsys fs.FS, name string, mode uint32) error
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

func NewService() *Service {
	return &Service{}
}

func (s *Service) NewBuilder() *Builder {
	return &Builder{}
}

func (s *Service) Build(configure func(*Builder) error) ([]File, error) {
	builder := s.NewBuilder()
	if err := configure(builder); err != nil {
		return nil, err
	}
	return builder.Files(), nil
}

func (b *Builder) AddBytes(path string, data []byte, mode uint32) error {
	path, err := cleanPath(path)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o400
	}
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
