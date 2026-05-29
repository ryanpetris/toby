package staticfiles

import (
	"io/fs"

	"petris.dev/toby/internal/staticmount"
)

type Registrar interface {
	AddBytes(path string, data []byte, mode uint32) error
	AddFS(path string, fsys fs.FS, name string, mode uint32) error
}

type Service struct{}

type Builder struct {
	files []staticmount.File
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) NewBuilder() *Builder {
	return &Builder{}
}

func (s *Service) NewMount(id, basePath string, configure func(*Builder) error) (*staticmount.Mount, error) {
	builder := s.NewBuilder()
	if err := configure(builder); err != nil {
		_ = builder.Close()
		return nil, err
	}
	mount, err := staticmount.New(id, basePath, builder.files)
	if err != nil {
		_ = builder.Close()
		return nil, err
	}
	builder.files = nil
	return mount, nil
}

func (b *Builder) AddBytes(path string, data []byte, mode uint32) error {
	b.files = append(b.files, staticmount.Bytes(path, data, mode))
	return nil
}

func (b *Builder) AddFS(path string, fsys fs.FS, name string, mode uint32) error {
	file, err := staticmount.FromFS(path, fsys, name, mode)
	if err != nil {
		return err
	}
	b.files = append(b.files, file)
	return nil
}

func (b *Builder) AddCurrentExecutable(path string, mode uint32) error {
	file, err := staticmount.CurrentExecutable(path, mode)
	if err != nil {
		return err
	}
	b.files = append(b.files, file)
	return nil
}

func (b *Builder) Files() []staticmount.File {
	return append([]staticmount.File(nil), b.files...)
}

func (b *Builder) Close() error {
	err := staticmount.CloseFiles(b.files)
	b.files = nil
	return err
}
