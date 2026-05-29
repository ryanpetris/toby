package fusekit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

type PassthroughOptions struct {
	ID       string
	BasePath string
	Source   string
	ReadOnly bool
}

type PassthroughMount struct {
	id       string
	basePath string
	source   string
	readOnly bool
}

func NewPassthroughMount(opts PassthroughOptions) (*PassthroughMount, error) {
	base, err := NormalizeVirtualPath(opts.BasePath)
	if err != nil {
		return nil, err
	}
	return &PassthroughMount{id: opts.ID, basePath: base, source: opts.Source, readOnly: opts.ReadOnly}, nil
}

func (m *PassthroughMount) ID() string { return m.id }

func (m *PassthroughMount) BasePath() string { return m.basePath }

func (m *PassthroughMount) Source() string { return m.source }

func (m *PassthroughMount) ReadOnly() bool { return m.readOnly }

func (m *PassthroughMount) Handle(ctx context.Context, op Operation, next Next) (Result, error) {
	switch op.Kind {
	case OpGetAttr:
		return m.getAttr(op.Path)
	case OpReadDir:
		return m.readDir(op.Path)
	case OpOpen:
		return m.open(ctx, op.Path, op.Flags, op.Mode)
	case OpCreate:
		return m.create(ctx, op.Path, op.Flags, op.Mode)
	case OpMkdir:
		return m.mkdir(op.Path, op.Mode)
	case OpUnlink:
		return Result{}, m.mutate(func() error { return syscall.Unlink(m.hostPath(op.Path)) })
	case OpRmdir:
		return Result{}, m.mutate(func() error { return syscall.Rmdir(m.hostPath(op.Path)) })
	case OpRename:
		return Result{}, m.rename(op.OldPath, op.NewPath)
	case OpReadlink:
		return m.readlink(op.Path)
	case OpSymlink:
		return m.symlink(op.Target, op.Path)
	case OpSetAttr:
		return m.setAttr(op.Path, op.SetAttr)
	case OpMaterialize:
		return Result{}, m.materialize(op.Path)
	default:
		return next(ctx, op)
	}
}

func (m *PassthroughMount) hostPath(virtualPath string) string {
	rel, _ := relativeVirtual(m.basePath, virtualPath)
	if rel == "" {
		return m.source
	}
	return filepath.Join(m.source, filepath.FromSlash(rel))
}

func (m *PassthroughMount) getAttr(virtualPath string) (Result, error) {
	st, err := lstat(m.hostPath(virtualPath))
	if err != nil {
		return Result{}, err
	}
	attr := m.attrFromStat(st)
	return Result{Attr: &attr}, nil
}

func (m *PassthroughMount) readDir(virtualPath string) (Result, error) {
	entries, err := os.ReadDir(m.hostPath(virtualPath))
	if err != nil {
		return Result{}, err
	}
	result := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		st, err := lstat(filepath.Join(m.hostPath(virtualPath), entry.Name()))
		if err != nil {
			return Result{}, err
		}
		result = append(result, DirEntry{
			Name:   entry.Name(),
			Object: m.objectKey(st),
			Mode:   uint32(st.Mode),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return Result{Entries: result}, nil
}

func (m *PassthroughMount) open(ctx context.Context, virtualPath string, flags, mode uint32) (Result, error) {
	if m.readOnly && writeFlags(flags) {
		return Result{}, syscall.EROFS
	}
	flags = openFlags(flags)
	hostPath := m.hostPath(virtualPath)
	fd, st, err := openHostFile(ctx, hostPath, flags, mode, false)
	if err != nil {
		return Result{}, err
	}
	attr := m.attrFromStat(st)
	return Result{Attr: &attr, Handle: &passthroughFile{mount: m, fd: fd, hostPath: hostPath}}, nil
}

func (m *PassthroughMount) create(ctx context.Context, virtualPath string, flags, mode uint32) (Result, error) {
	if m.readOnly {
		return Result{}, syscall.EROFS
	}
	flags = openFlags(flags)
	hostPath := m.hostPath(virtualPath)
	fd, st, err := openHostFile(ctx, hostPath, flags, mode, true)
	if err != nil {
		return Result{}, err
	}
	attr := m.attrFromStat(st)
	return Result{Attr: &attr, Handle: &passthroughFile{mount: m, fd: fd, hostPath: hostPath}}, nil
}

func (m *PassthroughMount) mkdir(virtualPath string, mode uint32) (Result, error) {
	if m.readOnly {
		return Result{}, syscall.EROFS
	}
	if mode == 0 {
		mode = 0o777
	}
	if err := os.Mkdir(m.hostPath(virtualPath), os.FileMode(mode)); err != nil {
		return Result{}, err
	}
	return m.getAttr(virtualPath)
}

func (m *PassthroughMount) rename(oldPath, newPath string) error {
	if m.readOnly {
		return syscall.EROFS
	}
	if !pathMatchesBase(m.basePath, oldPath) || !pathMatchesBase(m.basePath, newPath) {
		return syscall.EXDEV
	}
	return syscall.Rename(m.hostPath(oldPath), m.hostPath(newPath))
}

func (m *PassthroughMount) readlink(virtualPath string) (Result, error) {
	target, err := os.Readlink(m.hostPath(virtualPath))
	if err != nil {
		return Result{}, err
	}
	return Result{Data: []byte(target)}, nil
}

func (m *PassthroughMount) symlink(target, virtualPath string) (Result, error) {
	if m.readOnly {
		return Result{}, syscall.EROFS
	}
	if err := os.Symlink(target, m.hostPath(virtualPath)); err != nil {
		return Result{}, err
	}
	return m.getAttr(virtualPath)
}

func (m *PassthroughMount) setAttr(virtualPath string, attr SetAttr) (Result, error) {
	if m.readOnly {
		return Result{}, syscall.EROFS
	}
	path := m.hostPath(virtualPath)
	if err := applySetAttr(path, attr); err != nil {
		return Result{}, err
	}
	return m.getAttr(virtualPath)
}

func (m *PassthroughMount) materialize(virtualPath string) error {
	if m.readOnly {
		return syscall.EROFS
	}
	return os.MkdirAll(m.hostPath(virtualPath), 0o755)
}

func (m *PassthroughMount) mutate(fn func() error) error {
	if m.readOnly {
		return syscall.EROFS
	}
	return fn()
}

func (m *PassthroughMount) attrFromStat(st *syscall.Stat_t) Attr {
	return Attr{
		Object:  m.objectKey(st),
		Mode:    uint32(st.Mode),
		Size:    uint64(st.Size),
		UID:     st.Uid,
		GID:     st.Gid,
		Nlink:   uint32(st.Nlink),
		Rdev:    uint32(st.Rdev),
		Blocks:  uint64(st.Blocks),
		Blksize: uint32(st.Blksize),
		ATime:   timespecTime(st.Atim),
		MTime:   timespecTime(st.Mtim),
		CTime:   timespecTime(st.Ctim),
	}
}

func (m *PassthroughMount) objectKey(st *syscall.Stat_t) ObjectKey {
	return ObjectKey{MountID: m.id, Kind: "passthrough", Key: fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino))}
}

func lstat(path string) (*syscall.Stat_t, error) {
	st := syscall.Stat_t{}
	if err := syscall.Lstat(path, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeFlags(flags uint32) bool {
	access := flags & syscall.O_ACCMODE
	return access == syscall.O_WRONLY || access == syscall.O_RDWR || flags&(syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
}

func openFlags(flags uint32) uint32 {
	return flags &^ (syscall.O_APPEND | gofuse.FMODE_EXEC)
}

func openHostFile(ctx context.Context, path string, flags, mode uint32, create bool) (int, *syscall.Stat_t, error) {
	openFlag := int(flags | syscall.O_NONBLOCK)
	if create {
		openFlag |= os.O_CREATE
	}
	fd, err := syscall.Open(path, openFlag, mode)
	if err != nil {
		return -1, nil, err
	}
	st := syscall.Stat_t{}
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return -1, nil, err
	}
	fileType := uint32(st.Mode) & syscall.S_IFMT
	if fileType != syscall.S_IFREG {
		_ = syscall.Close(fd)
		err := syscall.EOPNOTSUPP
		if fileType == syscall.S_IFDIR {
			err = syscall.EISDIR
		}
		return -1, nil, err
	}
	if flags&syscall.O_NONBLOCK == 0 {
		_ = unix.SetNonblock(fd, false)
	}
	return fd, &st, nil
}

func applySetAttr(path string, attr SetAttr) error {
	if attr.Mode != nil {
		if err := syscall.Chmod(path, *attr.Mode); err != nil {
			return err
		}
	}
	if attr.UID != nil || attr.GID != nil {
		uid := -1
		gid := -1
		if attr.UID != nil {
			uid = int(*attr.UID)
		}
		if attr.GID != nil {
			gid = int(*attr.GID)
		}
		if err := unix.Fchownat(unix.AT_FDCWD, path, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
	}
	if attr.ATime != nil || attr.MTime != nil {
		atime := unix.Timespec{Nsec: unix.UTIME_OMIT}
		mtime := unix.Timespec{Nsec: unix.UTIME_OMIT}
		var err error
		if attr.ATime != nil {
			atime, err = unix.TimeToTimespec(*attr.ATime)
			if err != nil {
				return err
			}
		}
		if attr.MTime != nil {
			mtime, err = unix.TimeToTimespec(*attr.MTime)
			if err != nil {
				return err
			}
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, []unix.Timespec{atime, mtime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
	}
	if attr.Size != nil {
		if err := syscall.Truncate(path, int64(*attr.Size)); err != nil {
			return err
		}
	}
	return nil
}

func timespecTime(ts syscall.Timespec) time.Time {
	return time.Unix(int64(ts.Sec), int64(ts.Nsec))
}

type passthroughFile struct {
	mu       sync.Mutex
	mount    *PassthroughMount
	fd       int
	hostPath string
}

func (f *passthroughFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
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

func (f *passthroughFile) Write(ctx context.Context, data []byte, off int64) (uint32, error) {
	if f.mount.readOnly {
		return 0, syscall.EROFS
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return 0, syscall.EBADF
	}
	n, err := syscall.Pwrite(f.fd, data, off)
	return uint32(n), err
}

func (f *passthroughFile) Flush(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return syscall.EBADF
	}
	dup, err := syscall.Dup(f.fd)
	if err != nil {
		return err
	}
	return syscall.Close(dup)
}

func (f *passthroughFile) Fsync(ctx context.Context, flags uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return syscall.EBADF
	}
	return syscall.Fsync(f.fd)
}

func (f *passthroughFile) Release(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return syscall.EBADF
	}
	fd := f.fd
	f.fd = -1
	return syscall.Close(fd)
}

func (f *passthroughFile) GetAttr(ctx context.Context) (Attr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return Attr{}, syscall.EBADF
	}
	st := syscall.Stat_t{}
	if err := syscall.Fstat(f.fd, &st); err != nil {
		return Attr{}, err
	}
	return f.mount.attrFromStat(&st), nil
}

func (f *passthroughFile) SetAttr(ctx context.Context, attr SetAttr) (Attr, error) {
	if f.mount.readOnly {
		return Attr{}, syscall.EROFS
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fd < 0 {
		return Attr{}, syscall.EBADF
	}
	if attr.Mode != nil {
		if err := syscall.Fchmod(f.fd, *attr.Mode); err != nil {
			return Attr{}, err
		}
	}
	if attr.Size != nil {
		if err := syscall.Ftruncate(f.fd, int64(*attr.Size)); err != nil {
			return Attr{}, err
		}
	}
	st := syscall.Stat_t{}
	if err := syscall.Fstat(f.fd, &st); err != nil {
		return Attr{}, err
	}
	return f.mount.attrFromStat(&st), nil
}
