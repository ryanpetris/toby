package fusekit

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
)

type Server struct {
	Mountpoint string
	server     *gofuse.Server
	router     *Router
}

func MountServer(ctx context.Context, mountpoint string, router *Router) (*Server, error) {
	attrResult, err := router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: "/"})
	if err != nil {
		return nil, err
	}
	rootAttr := attrResult.Attr
	if rootAttr == nil {
		return nil, syscall.ENOENT
	}
	invalidator := newGoFuseInvalidator()
	router.SetInvalidator(invalidator)
	root := &fuseNode{router: router, invalidator: invalidator, key: rootAttr.Object}
	shortTTL := 100 * time.Millisecond
	negativeTTL := 10 * time.Millisecond
	logger, debug := fuseLogger()
	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions:    gofuse.MountOptions{Logger: logger, Debug: debug},
		AttrTimeout:     &shortTTL,
		EntryTimeout:    &shortTTL,
		NegativeTimeout: &negativeTTL,
		Logger:          logger,
		RootStableAttr:  &fs.StableAttr{Mode: rootAttr.Mode & syscall.S_IFMT, Ino: rootAttr.Inode},
	})
	if err != nil {
		router.SetInvalidator(NoopInvalidator{})
		return nil, err
	}
	return &Server{Mountpoint: mountpoint, server: server, router: router}, nil
}

func fuseLogger() (*log.Logger, bool) {
	debug := fuseDebugEnabled()
	if debug {
		return log.New(os.Stderr, "", log.LstdFlags), true
	}
	return log.New(io.Discard, "", 0), false
}

func fuseDebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("TOBY_FUSE_DEBUG")))
	switch value {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (s *Server) Unmount() error {
	if s == nil || s.server == nil {
		return nil
	}
	err := s.server.Unmount()
	s.router.SetInvalidator(NoopInvalidator{})
	return err
}

func (s *Server) Wait() {
	if s != nil && s.server != nil {
		s.server.Wait()
	}
}

type fuseNode struct {
	fs.Inode
	router      *Router
	invalidator *goFuseInvalidator
	key         ObjectKey
}

var _ = (fs.NodeOnAdder)((*fuseNode)(nil))
var _ = (fs.NodeAccesser)((*fuseNode)(nil))
var _ = (fs.NodeLookuper)((*fuseNode)(nil))
var _ = (fs.NodeGetattrer)((*fuseNode)(nil))
var _ = (fs.NodeReaddirer)((*fuseNode)(nil))
var _ = (fs.NodeOpener)((*fuseNode)(nil))
var _ = (fs.NodeCreater)((*fuseNode)(nil))
var _ = (fs.NodeMkdirer)((*fuseNode)(nil))
var _ = (fs.NodeUnlinker)((*fuseNode)(nil))
var _ = (fs.NodeRmdirer)((*fuseNode)(nil))
var _ = (fs.NodeRenamer)((*fuseNode)(nil))
var _ = (fs.NodeReadlinker)((*fuseNode)(nil))
var _ = (fs.NodeSymlinker)((*fuseNode)(nil))
var _ = (fs.NodeSetattrer)((*fuseNode)(nil))

func (n *fuseNode) OnAdd(ctx context.Context) {
	path := n.virtualPath()
	n.invalidator.register(path, n.key, n.EmbeddedInode())
}

func (n *fuseNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	return syscall.ENOSYS
}

func (n *fuseNode) Lookup(ctx context.Context, name string, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := joinVirtual(n.virtualPath(), name)
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: path})
	if err != nil {
		return nil, ErrnoOf(err)
	}
	if res.Attr == nil {
		return nil, syscall.ENOENT
	}
	fillEntry(out, *res.Attr)
	child := &fuseNode{router: n.router, invalidator: n.invalidator, key: res.Attr.Object}
	inode := n.NewInode(ctx, child, stableAttr(*res.Attr))
	n.invalidator.register(path, res.Attr.Object, inode)
	return inode, 0
}

func (n *fuseNode) Getattr(ctx context.Context, fh fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	path := n.virtualPath()
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpGetAttr, Path: path})
	if err != nil {
		return ErrnoOf(err)
	}
	if res.Attr == nil {
		return syscall.ENOENT
	}
	fillAttr(&out.Attr, *res.Attr)
	return 0
}

func (n *fuseNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	path := n.virtualPath()
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpReadDir, Path: path})
	if err != nil {
		return nil, ErrnoOf(err)
	}
	entries := make([]gofuse.DirEntry, 0, len(res.Entries))
	for _, entry := range res.Entries {
		entries = append(entries, gofuse.DirEntry{Name: entry.Name, Ino: entry.Inode, Mode: entry.Mode})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *fuseNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	path := n.virtualPath()
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpOpen, Path: path, Flags: flags})
	if err != nil {
		return nil, 0, ErrnoOf(err)
	}
	return &fuseHandle{inner: res.Handle, path: path}, res.FuseFlags, 0
}

func (n *fuseNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *gofuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	path := joinVirtual(n.virtualPath(), name)
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpCreate, Path: path, Flags: flags, Mode: mode})
	if err != nil {
		return nil, nil, 0, ErrnoOf(err)
	}
	if res.Attr == nil {
		return nil, nil, 0, syscall.EIO
	}
	fillEntry(out, *res.Attr)
	child := &fuseNode{router: n.router, invalidator: n.invalidator, key: res.Attr.Object}
	inode := n.NewInode(ctx, child, stableAttr(*res.Attr))
	n.invalidator.register(path, res.Attr.Object, inode)
	return inode, &fuseHandle{inner: res.Handle, path: path}, res.FuseFlags, 0
}

func (n *fuseNode) Mkdir(ctx context.Context, name string, mode uint32, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := joinVirtual(n.virtualPath(), name)
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpMkdir, Path: path, Mode: mode})
	if err != nil {
		return nil, ErrnoOf(err)
	}
	if res.Attr == nil {
		return nil, syscall.EIO
	}
	fillEntry(out, *res.Attr)
	child := &fuseNode{router: n.router, invalidator: n.invalidator, key: res.Attr.Object}
	inode := n.NewInode(ctx, child, stableAttr(*res.Attr))
	n.invalidator.register(path, res.Attr.Object, inode)
	return inode, 0
}

func (n *fuseNode) Unlink(ctx context.Context, name string) syscall.Errno {
	path := joinVirtual(n.virtualPath(), name)
	_, err := n.router.Dispatch(ctx, Operation{Kind: OpUnlink, Path: path})
	return ErrnoOf(err)
}

func (n *fuseNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	path := joinVirtual(n.virtualPath(), name)
	_, err := n.router.Dispatch(ctx, Operation{Kind: OpRmdir, Path: path})
	return ErrnoOf(err)
}

func (n *fuseNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	newNode, ok := newParent.(*fuseNode)
	if !ok {
		return syscall.EXDEV
	}
	oldPath := joinVirtual(n.virtualPath(), name)
	newPath := joinVirtual(newNode.virtualPath(), newName)
	_, err := n.router.Dispatch(ctx, Operation{Kind: OpRename, OldPath: oldPath, NewPath: newPath, Flags: flags})
	return ErrnoOf(err)
}

func (n *fuseNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	path := n.virtualPath()
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpReadlink, Path: path})
	if err != nil {
		return nil, ErrnoOf(err)
	}
	return res.Data, 0
}

func (n *fuseNode) Symlink(ctx context.Context, target, name string, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := joinVirtual(n.virtualPath(), name)
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpSymlink, Path: path, Target: target})
	if err != nil {
		return nil, ErrnoOf(err)
	}
	if res.Attr == nil {
		return nil, syscall.EIO
	}
	fillEntry(out, *res.Attr)
	child := &fuseNode{router: n.router, invalidator: n.invalidator, key: res.Attr.Object}
	inode := n.NewInode(ctx, child, stableAttr(*res.Attr))
	n.invalidator.register(path, res.Attr.Object, inode)
	return inode, 0
}

func (n *fuseNode) Setattr(ctx context.Context, fh fs.FileHandle, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	path := n.virtualPath()
	setAttr := SetAttr{}
	if mode, ok := in.GetMode(); ok {
		setAttr.Mode = &mode
	}
	if uid, ok := in.GetUID(); ok {
		setAttr.UID = &uid
	}
	if gid, ok := in.GetGID(); ok {
		setAttr.GID = &gid
	}
	if size, ok := in.GetSize(); ok {
		setAttr.Size = &size
	}
	if atime, ok := in.GetATime(); ok {
		setAttr.ATime = &atime
	}
	if mtime, ok := in.GetMTime(); ok {
		setAttr.MTime = &mtime
	}
	res, err := n.router.Dispatch(ctx, Operation{Kind: OpSetAttr, Path: path, SetAttr: setAttr})
	if err != nil {
		return ErrnoOf(err)
	}
	if res.Attr == nil {
		return syscall.EIO
	}
	fillAttr(&out.Attr, *res.Attr)
	return 0
}

func (n *fuseNode) virtualPath() string {
	if n.EmbeddedInode().IsRoot() {
		return "/"
	}
	return mustNormalizeVirtualPath("/" + n.EmbeddedInode().Path(n.EmbeddedInode().Root()))
}

type fuseHandle struct {
	inner FileHandle
	path  string
}

var _ = (fs.FileReader)((*fuseHandle)(nil))
var _ = (fs.FileWriter)((*fuseHandle)(nil))
var _ = (fs.FileFlusher)((*fuseHandle)(nil))
var _ = (fs.FileFsyncer)((*fuseHandle)(nil))
var _ = (fs.FileReleaser)((*fuseHandle)(nil))

func (h *fuseHandle) Read(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	reader, ok := h.inner.(FileReader)
	if !ok {
		return nil, syscall.ENOSYS
	}
	data, err := reader.Read(ctx, dest, off)
	if err != nil {
		return nil, ErrnoOf(err)
	}
	return gofuse.ReadResultData(data), 0
}

func (h *fuseHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	writer, ok := h.inner.(FileWriter)
	if !ok {
		return 0, syscall.ENOSYS
	}
	written, err := writer.Write(ctx, data, off)
	return written, ErrnoOf(err)
}

func (h *fuseHandle) Flush(ctx context.Context) syscall.Errno {
	flusher, ok := h.inner.(FileFlusher)
	if !ok {
		return 0
	}
	err := flusher.Flush(ctx)
	return ErrnoOf(err)
}

func (h *fuseHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	fsyncer, ok := h.inner.(FileFsyncer)
	if !ok {
		return 0
	}
	err := fsyncer.Fsync(ctx, flags)
	return ErrnoOf(err)
}

func (h *fuseHandle) Release(ctx context.Context) syscall.Errno {
	releaser, ok := h.inner.(FileReleaser)
	if !ok {
		return 0
	}
	err := releaser.Release(ctx)
	return ErrnoOf(err)
}

func stableAttr(attr Attr) fs.StableAttr {
	return fs.StableAttr{Mode: attr.Mode & syscall.S_IFMT, Ino: attr.Inode}
}

func fillEntry(out *gofuse.EntryOut, attr Attr) {
	fillAttr(&out.Attr, attr)
	out.NodeId = attr.Inode
	out.Generation = 1
}

func fillAttr(out *gofuse.Attr, attr Attr) {
	out.Ino = attr.Inode
	out.Size = attr.Size
	out.Blocks = attr.Blocks
	out.Atime = uint64(attr.ATime.Unix())
	out.Mtime = uint64(attr.MTime.Unix())
	out.Ctime = uint64(attr.CTime.Unix())
	out.Atimensec = uint32(attr.ATime.Nanosecond())
	out.Mtimensec = uint32(attr.MTime.Nanosecond())
	out.Ctimensec = uint32(attr.CTime.Nanosecond())
	out.Mode = attr.Mode
	out.Nlink = attr.Nlink
	out.Uid = attr.UID
	out.Gid = attr.GID
	out.Rdev = attr.Rdev
	out.Blksize = attr.Blksize
}

type goFuseInvalidator struct {
	mu        sync.Mutex
	pathNodes map[string]*fs.Inode
	keyNodes  map[ObjectKey]map[*fs.Inode]struct{}
}

func newGoFuseInvalidator() *goFuseInvalidator {
	return &goFuseInvalidator{pathNodes: map[string]*fs.Inode{}, keyNodes: map[ObjectKey]map[*fs.Inode]struct{}{}}
}

func (i *goFuseInvalidator) register(path string, key ObjectKey, node *fs.Inode) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.pathNodes[path] = node
	if !key.empty() {
		if i.keyNodes[key] == nil {
			i.keyNodes[key] = map[*fs.Inode]struct{}{}
		}
		i.keyNodes[key][node] = struct{}{}
	}
}

func (i *goFuseInvalidator) EntryChanged(parentPath, name string) {
	i.mu.Lock()
	parent := i.pathNodes[parentPath]
	i.mu.Unlock()
	if parent == nil {
		return
	}
	go func() {
		_ = parent.NotifyEntry(name)
	}()
}

func (i *goFuseInvalidator) InodeChanged(key ObjectKey) {
	i.mu.Lock()
	nodes := make([]*fs.Inode, 0, len(i.keyNodes[key]))
	for node := range i.keyNodes[key] {
		nodes = append(nodes, node)
	}
	i.mu.Unlock()
	if len(nodes) == 0 {
		return
	}
	for _, node := range nodes {
		node := node
		go func() {
			_ = node.NotifyContent(0, 0)
		}()
	}
}
