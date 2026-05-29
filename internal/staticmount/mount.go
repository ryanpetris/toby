package staticmount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/fusekit"
)

type File struct {
	Path   string
	Data   []byte
	Mode   uint32
	Source Source
}

type Source interface {
	Size() (uint64, error)
	Open() (fusekit.FileHandle, error)
}

type sourceCloser interface {
	Close() error
}

type Mount struct {
	id      string
	base    string
	files   map[string]File
	dirs    map[string]bool
	created time.Time
}

type readOnlyFile struct {
	data []byte
}

type fsSource struct {
	fsys fs.FS
	name string
}

type fsFile struct {
	fsys fs.FS
	name string
}

type hostFDSource struct {
	mu sync.Mutex
	fd int
}

type hostFile struct {
	mu sync.Mutex
	fd int
}

var _ fusekit.FileReader = (*readOnlyFile)(nil)
var _ fusekit.FileReader = (*fsFile)(nil)
var _ fusekit.FileReader = (*hostFile)(nil)
var _ fusekit.FileReleaser = (*hostFile)(nil)

func Bytes(path string, data []byte, mode uint32) File {
	return File{Path: path, Data: data, Mode: mode}
}

func FromFS(path string, fsys fs.FS, name string, mode uint32) (File, error) {
	if fsys == nil {
		return File{}, fmt.Errorf("fs is required")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || !fs.ValidPath(name) {
		return File{}, fmt.Errorf("invalid fs path")
	}
	info, err := fs.Stat(fsys, name)
	if err != nil {
		return File{}, err
	}
	if info.IsDir() {
		return File{}, fmt.Errorf("fs path is a directory: %s", name)
	}
	return File{Path: path, Mode: mode, Source: fsSource{fsys: fsys, name: name}}, nil
}

func CurrentExecutable(path string, mode uint32) (File, error) {
	fd, err := syscall.Open("/proc/self/exe", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return File{}, err
	}
	return File{Path: path, Mode: mode, Source: &hostFDSource{fd: fd}}, nil
}

func New(id, basePath string, files []File) (*Mount, error) {
	base, err := fusekit.NormalizeVirtualPath(basePath)
	if err != nil {
		return nil, err
	}
	m := &Mount{id: id, base: base, files: map[string]File{}, dirs: map[string]bool{"/": true}, created: time.Now()}
	for _, file := range files {
		rel, err := cleanRelative(file.Path)
		if err != nil {
			_ = closeFile(file)
			_ = m.Close()
			return nil, fmt.Errorf("static file %q: %w", file.Path, err)
		}
		if file.Mode == 0 {
			file.Mode = 0o400
		}
		if file.Source != nil {
			if _, err := file.Source.Size(); err != nil {
				_ = closeFile(file)
				_ = m.Close()
				return nil, fmt.Errorf("static file %q: %w", file.Path, err)
			}
		} else {
			file.Data = append([]byte(nil), file.Data...)
		}
		file.Path = rel
		if old, ok := m.files["/"+rel]; ok {
			_ = closeFile(old)
		}
		m.files["/"+rel] = file
		for dir := pathpkg.Dir("/" + rel); dir != "." && dir != "/"; dir = pathpkg.Dir(dir) {
			m.dirs[dir] = true
		}
	}
	return m, nil
}

func (m *Mount) ID() string { return m.id }

func (m *Mount) BasePath() string { return m.base }

func (m *Mount) Close() error {
	if m == nil {
		return nil
	}
	files := make([]File, 0, len(m.files))
	for _, file := range m.files {
		files = append(files, file)
	}
	m.files = map[string]File{}
	return CloseFiles(files)
}

func CloseFiles(files []File) error {
	var errs []error
	for _, file := range files {
		if err := closeFile(file); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Mount) Handle(ctx context.Context, op fusekit.Operation, next fusekit.Next) (fusekit.Result, error) {
	if !pathWithin(m.base, op.PrimaryPath()) {
		return next(ctx, op)
	}
	switch op.Kind {
	case fusekit.OpGetAttr:
		return m.getAttr(op.Path)
	case fusekit.OpReadDir:
		return m.readDir(op.Path)
	case fusekit.OpOpen:
		return m.open(op.Path, op.Flags)
	case fusekit.OpCreate, fusekit.OpMkdir, fusekit.OpUnlink, fusekit.OpRmdir, fusekit.OpRename, fusekit.OpSymlink, fusekit.OpSetAttr, fusekit.OpMaterialize:
		return fusekit.Result{}, syscall.EROFS
	default:
		return fusekit.Result{}, syscall.ENOENT
	}
}

func (m *Mount) getAttr(path string) (fusekit.Result, error) {
	rel := m.relative(path)
	if m.dirs[rel] {
		attr := m.attr("dir", rel, syscall.S_IFDIR|0o500, 0)
		return fusekit.Result{Attr: &attr}, nil
	}
	file, ok := m.files[rel]
	if !ok {
		return fusekit.Result{}, syscall.ENOENT
	}
	size, err := file.size()
	if err != nil {
		return fusekit.Result{}, err
	}
	attr := m.attr("file", rel, syscall.S_IFREG|(file.Mode&0o777), size)
	return fusekit.Result{Attr: &attr}, nil
}

func (m *Mount) readDir(path string) (fusekit.Result, error) {
	rel := m.relative(path)
	if !m.dirs[rel] {
		return fusekit.Result{}, syscall.ENOTDIR
	}
	seen := map[string]fusekit.DirEntry{}
	for dir := range m.dirs {
		if dir == rel || pathpkg.Dir(dir) != rel {
			continue
		}
		name := pathpkg.Base(dir)
		seen[name] = fusekit.DirEntry{Name: name, Object: fusekit.ObjectKey{MountID: m.id, Kind: "dir", Key: dir}, Mode: syscall.S_IFDIR | 0o500}
	}
	for relPath, file := range m.files {
		if pathpkg.Dir(relPath) != rel {
			continue
		}
		name := pathpkg.Base(relPath)
		seen[name] = fusekit.DirEntry{Name: name, Object: fusekit.ObjectKey{MountID: m.id, Kind: "file", Key: relPath}, Mode: syscall.S_IFREG | (file.Mode & 0o777)}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]fusekit.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, seen[name])
	}
	return fusekit.Result{Entries: entries}, nil
}

func (m *Mount) open(path string, flags uint32) (fusekit.Result, error) {
	if hasWriteFlags(flags) {
		return fusekit.Result{}, syscall.EROFS
	}
	rel := m.relative(path)
	file, ok := m.files[rel]
	if !ok {
		if m.dirs[rel] {
			return fusekit.Result{}, syscall.EISDIR
		}
		return fusekit.Result{}, syscall.ENOENT
	}
	handle, err := file.open()
	if err != nil {
		return fusekit.Result{}, err
	}
	size, err := file.size()
	if err != nil {
		if releaser, ok := handle.(fusekit.FileReleaser); ok {
			_ = releaser.Release(context.Background())
		}
		return fusekit.Result{}, err
	}
	attr := m.attr("file", rel, syscall.S_IFREG|(file.Mode&0o777), size)
	return fusekit.Result{Attr: &attr, Handle: handle}, nil
}

func (f File) size() (uint64, error) {
	if f.Source != nil {
		return f.Source.Size()
	}
	return uint64(len(f.Data)), nil
}

func (f File) open() (fusekit.FileHandle, error) {
	if f.Source != nil {
		return f.Source.Open()
	}
	return &readOnlyFile{data: append([]byte(nil), f.Data...)}, nil
}

func (m *Mount) relative(path string) string {
	if path == m.base {
		return "/"
	}
	return strings.TrimPrefix(path, m.base)
}

func (m *Mount) attr(kind, key string, mode uint32, size uint64) fusekit.Attr {
	return fusekit.Attr{
		Object: fusekit.ObjectKey{MountID: m.id, Kind: kind, Key: key},
		Mode:   mode,
		Size:   size,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  1,
		ATime:  m.created,
		MTime:  m.created,
		CTime:  m.created,
	}
}

func (f *readOnlyFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if int64(len(f.data)) <= off {
		return nil, nil
	}
	data := f.data[off:]
	if len(data) > len(dest) {
		data = data[:len(dest)]
	}
	return append([]byte(nil), data...), nil
}

func (s fsSource) Size() (uint64, error) {
	info, err := fs.Stat(s.fsys, s.name)
	if err != nil {
		return 0, err
	}
	return uint64(info.Size()), nil
}

func (s fsSource) Open() (fusekit.FileHandle, error) {
	file, err := s.fsys.Open(s.name)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	return &fsFile{fsys: s.fsys, name: s.name}, nil
}

func (f *fsFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if len(dest) == 0 {
		return nil, nil
	}
	file, err := f.fsys.Open(f.name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if readerAt, ok := file.(io.ReaderAt); ok {
		n, err := readerAt.ReadAt(dest, off)
		if errors.Is(err, io.EOF) {
			if n == 0 {
				return nil, nil
			}
			err = nil
		}
		return append([]byte(nil), dest[:n]...), err
	}
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	} else if off > 0 {
		if _, err := io.CopyN(io.Discard, file, off); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
	}
	n, err := file.Read(dest)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return append([]byte(nil), dest[:n]...), err
}

func (s *hostFDSource) Size() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return 0, syscall.EBADF
	}
	st := syscall.Stat_t{}
	if err := syscall.Fstat(s.fd, &st); err != nil {
		return 0, err
	}
	return uint64(st.Size), nil
}

func (s *hostFDSource) Open() (fusekit.FileHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return nil, syscall.EBADF
	}
	fd, err := syscall.Dup(s.fd)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	return &hostFile{fd: fd}, nil
}

func (s *hostFDSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return nil
	}
	fd := s.fd
	s.fd = -1
	return syscall.Close(fd)
}

func (f *hostFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if f.fd < 0 {
		return nil, syscall.EBADF
	}
	buf := make([]byte, len(dest))
	n, err := syscall.Pread(f.fd, buf, off)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (f *hostFile) Release(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return nil
	}
	fd := f.fd
	f.fd = -1
	return syscall.Close(fd)
}

func closeFile(file File) error {
	closer, ok := file.Source.(sourceCloser)
	if !ok {
		return nil
	}
	return closer.Close()
}

func cleanRelative(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || pathpkg.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("invalid relative path")
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid relative path segment")
		}
	}
	return strings.Join(segments, "/"), nil
}

func pathWithin(base, path string) bool {
	return path == base || strings.HasPrefix(path, base+"/")
}

func hasWriteFlags(flags uint32) bool {
	access := flags & syscall.O_ACCMODE
	return access == syscall.O_WRONLY || access == syscall.O_RDWR || flags&(syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
}
