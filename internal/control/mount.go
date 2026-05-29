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
	ControlPath = "/.local/state/toby/control"
	maxRequest  = 4096
)

type mountPaths struct {
	base    string
	control string
}

type Handler func(context.Context, []byte) ([]byte, error)

type Mount struct {
	handler           Handler
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

func (m *Mount) Close() error {
	return nil
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
	case m.paths.control:
		attr = m.attr("file", path, syscall.S_IFREG|0o600)
		attr.Size = maxRequest
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

func defaultMountPaths() mountPaths {
	return mountPaths{
		base:    BasePath,
		control: ControlPath,
	}
}

func newMountPaths(basePath string) (mountPaths, error) {
	base, err := fusekit.NormalizeVirtualPath(basePath)
	if err != nil {
		return mountPaths{}, err
	}
	return mountPaths{
		base:    base,
		control: pathpkg.Join(base, "control"),
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
