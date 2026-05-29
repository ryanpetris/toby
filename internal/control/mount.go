package control

import (
	"bytes"
	"context"
	"os"
	pathpkg "path"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/fusekit"
)

const (
	BasePath    = "/.local/state/toby"
	BinPath     = "/.local/state/toby/bin"
	BinaryPath  = "/.local/state/toby/bin/toby"
	ControlPath = "/.local/state/toby/control"
	StaticPath  = "/.local/state/toby/static"
	selfExePath = "/proc/self/exe"
	maxRequest  = 4096
)

type mountPaths struct {
	base    string
	bin     string
	binary  string
	control string
	static  string
}

type binaryFile struct {
	fd int
}

type Handler func(context.Context, []byte) ([]byte, error)

type Mount struct {
	handler           Handler
	binary            *binaryFile
	paths             mountPaths
	created           time.Time
	mountableProjects bool
}

type MountOption func(*Mount)

func WithMountableProjects(enabled bool) MountOption {
	return func(m *Mount) {
		m.mountableProjects = enabled
	}
}

func NewMount(handler Handler, opts ...MountOption) *Mount {
	mount := &Mount{handler: handler, paths: defaultMountPaths(), created: time.Now()}
	for _, opt := range opts {
		opt(mount)
	}
	return mount
}

func NewMountAt(basePath string, handler Handler, opts ...MountOption) (*Mount, error) {
	paths, err := newMountPaths(basePath)
	if err != nil {
		return nil, err
	}
	mount := &Mount{handler: handler, paths: paths, created: time.Now()}
	for _, opt := range opts {
		opt(mount)
	}
	return mount, nil
}

func NewMountWithCurrentBinary(handler Handler, opts ...MountOption) (*Mount, error) {
	return NewMountWithCurrentBinaryAt(BasePath, handler, opts...)
}

func NewMountWithCurrentBinaryAt(basePath string, handler Handler, opts ...MountOption) (*Mount, error) {
	fd, err := syscall.Open(selfExePath, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	mount, err := newMountWithBinaryFDAt(basePath, handler, fd, opts...)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return mount, nil
}

func newMountWithBinaryFDAt(basePath string, handler Handler, fd int, opts ...MountOption) (*Mount, error) {
	mount, err := NewMountAt(basePath, handler, opts...)
	if err != nil {
		return nil, err
	}
	attr, err := binaryFileAttr(mount.paths.binary, fd, mount.ID())
	if err != nil {
		return nil, err
	}
	if attr.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, os.ErrInvalid
	}
	mount.binary = &binaryFile{fd: fd}
	return mount, nil
}

func (m *Mount) Close() error {
	if m == nil || m.binary == nil {
		return nil
	}
	fd := m.binary.fd
	m.binary = nil
	return syscall.Close(fd)
}

func (m *Mount) ID() string { return "toby-control" }

func (m *Mount) BasePath() string { return m.paths.base }

func (m *Mount) Handle(ctx context.Context, op fusekit.Operation, next fusekit.Next) (fusekit.Result, error) {
	switch op.Kind {
	case fusekit.OpGetAttr:
		return m.getAttr(op.Path)
	case fusekit.OpReadDir:
		return m.readDir(op.Path)
	case fusekit.OpOpen:
		return m.open(op.Path, op.Flags)
	case fusekit.OpCreate:
		return m.create(op.Path)
	case fusekit.OpSetAttr:
		return fusekit.Result{}, syscall.EROFS
	case fusekit.OpMkdir, fusekit.OpUnlink, fusekit.OpRmdir, fusekit.OpRename, fusekit.OpSymlink, fusekit.OpMaterialize:
		return fusekit.Result{}, syscall.EROFS
	default:
		return fusekit.Result{}, syscall.ENOENT
	}
}

func (m *Mount) getAttr(path string) (fusekit.Result, error) {
	var attr fusekit.Attr
	switch path {
	case m.paths.base:
		attr = m.attr("dir", path, syscall.S_IFDIR|0o500)
	case m.paths.bin:
		attr = m.attr("dir", path, syscall.S_IFDIR|0o500)
	case m.paths.control:
		attr = m.attr("file", path, syscall.S_IFREG|0o600)
		attr.Size = maxRequest
	case m.paths.static:
		attr = m.attr("dir", path, syscall.S_IFDIR|0o500)
	case m.paths.binary:
		return m.binaryAttr()
	default:
		return fusekit.Result{}, syscall.ENOENT
	}
	return fusekit.Result{Attr: &attr}, nil
}

func (m *Mount) readDir(path string) (fusekit.Result, error) {
	switch path {
	case m.paths.base:
		entries := []fusekit.DirEntry{
			{
				Name:   "control",
				Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "file", Key: m.paths.control},
				Mode:   syscall.S_IFREG | 0o600,
			},
			{
				Name:   "bin",
				Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "dir", Key: m.paths.bin},
				Mode:   syscall.S_IFDIR | 0o500,
			},
			{
				Name:   "static",
				Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "dir", Key: m.paths.static},
				Mode:   syscall.S_IFDIR | 0o500,
			},
		}
		return fusekit.Result{Entries: entries}, nil
	case m.paths.static:
		return fusekit.Result{Entries: nil}, nil
	case m.paths.bin:
		entries := []fusekit.DirEntry{}
		if m.binary != nil {
			entries = append(entries, fusekit.DirEntry{
				Name:   "toby",
				Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "file", Key: m.paths.binary},
				Mode:   syscall.S_IFREG | 0o500,
			})
		}
		return fusekit.Result{Entries: entries}, nil
	default:
		return fusekit.Result{}, syscall.ENOTDIR
	}
}

func (m *Mount) open(path string, flags uint32) (fusekit.Result, error) {
	switch path {
	case m.paths.control:
		access := flags & syscall.O_ACCMODE
		if access == syscall.O_RDONLY {
			return fusekit.Result{}, syscall.EACCES
		}
		if flags&(syscall.O_CREAT|syscall.O_TRUNC|syscall.O_APPEND) != 0 {
			return fusekit.Result{}, syscall.EROFS
		}
		return fusekit.Result{Handle: &controlFile{handler: m.handler}}, nil
	case m.paths.binary:
		return m.openBinary(flags)
	default:
		if hasWriteFlags(flags) {
			return fusekit.Result{}, syscall.EROFS
		}
		return fusekit.Result{}, syscall.ENOENT
	}
}

func (m *Mount) create(path string) (fusekit.Result, error) {
	return fusekit.Result{}, syscall.EROFS
}

func (m *Mount) attr(kind, key string, mode uint32) fusekit.Attr {
	return fusekit.Attr{
		Object: fusekit.ObjectKey{MountID: m.ID(), Kind: kind, Key: key},
		Mode:   mode,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  1,
		ATime:  m.created,
		MTime:  m.created,
		CTime:  m.created,
	}
}

func (m *Mount) binaryAttr() (fusekit.Result, error) {
	if m.binary == nil {
		return fusekit.Result{}, syscall.ENOENT
	}
	attr, err := binaryFileAttr(m.paths.binary, m.binary.fd, m.ID())
	if err != nil {
		return fusekit.Result{}, err
	}
	return fusekit.Result{Attr: &attr}, nil
}

func (m *Mount) openBinary(flags uint32) (fusekit.Result, error) {
	if m.binary == nil {
		return fusekit.Result{}, syscall.ENOENT
	}
	if hasWriteFlags(flags) {
		return fusekit.Result{}, syscall.EROFS
	}
	fd, err := syscall.Dup(m.binary.fd)
	if err != nil {
		return fusekit.Result{}, err
	}
	syscall.CloseOnExec(fd)
	attr, err := binaryFileAttr(m.paths.binary, fd, m.ID())
	if err != nil {
		_ = syscall.Close(fd)
		return fusekit.Result{}, err
	}
	return fusekit.Result{Attr: &attr, Handle: &hostFile{fd: fd}}, nil
}

func binaryFileAttr(key string, fd int, mountID string) (fusekit.Attr, error) {
	st := syscall.Stat_t{}
	if err := syscall.Fstat(fd, &st); err != nil {
		return fusekit.Attr{}, err
	}
	return fusekit.Attr{
		Object:  fusekit.ObjectKey{MountID: mountID, Kind: "file", Key: key},
		Mode:    uint32(st.Mode)&syscall.S_IFMT | 0o500,
		Size:    uint64(st.Size),
		UID:     st.Uid,
		GID:     st.Gid,
		Nlink:   uint32(st.Nlink),
		Rdev:    uint32(st.Rdev),
		Blocks:  uint64(st.Blocks),
		Blksize: uint32(st.Blksize),
		ATime:   time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)),
		MTime:   time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)),
		CTime:   time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)),
	}, nil
}

func defaultMountPaths() mountPaths {
	return mountPaths{
		base:    BasePath,
		bin:     BinPath,
		binary:  BinaryPath,
		control: ControlPath,
		static:  StaticPath,
	}
}

func newMountPaths(basePath string) (mountPaths, error) {
	base, err := fusekit.NormalizeVirtualPath(basePath)
	if err != nil {
		return mountPaths{}, err
	}
	return mountPaths{
		base:    base,
		bin:     pathpkg.Join(base, "bin"),
		binary:  pathpkg.Join(base, "bin", "toby"),
		control: pathpkg.Join(base, "control"),
		static:  pathpkg.Join(base, "static"),
	}, nil
}

func hasWriteFlags(flags uint32) bool {
	access := flags & syscall.O_ACCMODE
	return access == syscall.O_WRONLY || access == syscall.O_RDWR || flags&(syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
}

type controlFile struct {
	mu        sync.Mutex
	handler   Handler
	buf       []byte
	response  []byte
	processed bool
	err       error
}

var _ = (fusekit.FileReader)((*controlFile)(nil))
var _ = (fusekit.FileWriter)((*controlFile)(nil))
var _ = (fusekit.FileFlusher)((*controlFile)(nil))
var _ = (fusekit.FileFsyncer)((*controlFile)(nil))

func (f *controlFile) Write(ctx context.Context, data []byte, off int64) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.processed {
		return 0, syscall.EINVAL
	}
	if len(f.buf)+len(data) > maxRequest {
		f.processed = true
		f.err = syscall.E2BIG
		return 0, f.err
	}
	f.buf = append(f.buf, data...)
	if bytes.Contains(f.buf, []byte{'\n'}) {
		if err := f.process(ctx, true); err != nil {
			return 0, err
		}
	}
	return uint32(len(data)), nil
}

func (f *controlFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if int64(len(f.response)) <= off {
		return nil, nil
	}
	data := f.response[off:]
	if len(data) > len(dest) {
		data = data[:len(dest)]
	}
	return append([]byte(nil), data...), nil
}

func (f *controlFile) Flush(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.processed {
		return f.err
	}
	if len(bytes.TrimSpace(f.buf)) == 0 {
		return nil
	}
	return f.process(ctx, false)
}

func (f *controlFile) Release(context.Context) error { return nil }

func (f *controlFile) process(ctx context.Context, requireSingleLine bool) error {
	f.processed = true
	request := f.buf
	if i := bytes.IndexByte(request, '\n'); i >= 0 {
		if requireSingleLine && len(bytes.TrimSpace(request[i+1:])) != 0 {
			f.err = syscall.EINVAL
			f.response = ResponseError(nil, CodeInvalidRequest, "control file accepts one JSON-RPC request per open handle", nil)
			return f.err
		}
		request = request[:i]
	}
	request = bytes.TrimSpace(request)
	if len(request) == 0 || bytes.Contains(request, []byte{'\n'}) {
		f.err = syscall.EINVAL
		f.response = ResponseError(nil, CodeInvalidRequest, "empty control request", nil)
		return f.err
	}
	if f.handler == nil {
		f.err = syscall.ENOSYS
		f.response = ResponseError(nil, CodeInternalError, "control handler is not configured", nil)
		return f.err
	}
	response, err := f.handler(ctx, request)
	if len(response) > 0 {
		f.response = append([]byte(nil), response...)
	}
	if err != nil {
		f.err = err
		return err
	}
	f.err = nil
	return nil
}

func (f *controlFile) Fsync(context.Context, uint32) error {
	return f.err
}

type staticFile struct {
	data []byte
}

var _ = (fusekit.FileReader)((*staticFile)(nil))

func (f *staticFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
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

type hostFile struct {
	mu sync.Mutex
	fd int
}

var _ = (fusekit.FileReader)((*hostFile)(nil))
var _ = (fusekit.FileReleaser)((*hostFile)(nil))

func (f *hostFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
