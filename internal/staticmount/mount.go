package staticmount

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"syscall"
	"time"

	"petris.dev/toby/fusekit"
)

type File struct {
	Path string
	Data []byte
	Mode uint32
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

var _ fusekit.FileReader = (*readOnlyFile)(nil)

func New(id, basePath string, files []File) (*Mount, error) {
	base, err := fusekit.NormalizeVirtualPath(basePath)
	if err != nil {
		return nil, err
	}
	m := &Mount{id: id, base: base, files: map[string]File{}, dirs: map[string]bool{"/": true}, created: time.Now()}
	for _, file := range files {
		rel, err := cleanRelative(file.Path)
		if err != nil {
			return nil, fmt.Errorf("static file %q: %w", file.Path, err)
		}
		if file.Mode == 0 {
			file.Mode = 0o400
		}
		file.Path = rel
		file.Data = append([]byte(nil), file.Data...)
		m.files["/"+rel] = file
		for dir := pathpkg.Dir("/" + rel); dir != "." && dir != "/"; dir = pathpkg.Dir(dir) {
			m.dirs[dir] = true
		}
	}
	return m, nil
}

func (m *Mount) ID() string { return m.id }

func (m *Mount) BasePath() string { return m.base }

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
	attr := m.attr("file", rel, syscall.S_IFREG|(file.Mode&0o777), uint64(len(file.Data)))
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
	attr := m.attr("file", rel, syscall.S_IFREG|(file.Mode&0o777), uint64(len(file.Data)))
	return fusekit.Result{Attr: &attr, Handle: &readOnlyFile{data: append([]byte(nil), file.Data...)}}, nil
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
