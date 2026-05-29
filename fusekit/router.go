package fusekit

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type RouterOptions struct {
	InodeCapacity int
	Invalidator   Invalidator
	StartTime     time.Time
}

type Router struct {
	snapshot    atomic.Pointer[snapshotData]
	inodes      *InodeTable
	startTime   time.Time
	invalidator Invalidator
	invMu       sync.RWMutex
}

type Snapshot struct {
	data *snapshotData
}

type snapshotData struct {
	mounts []mountEntry
}

type mountEntry struct {
	mount Mount
	base  string
	index int
}

func NewRouter(mounts []Mount) (*Router, error) {
	return NewRouterWithOptions(mounts, RouterOptions{})
}

func NewRouterWithOptions(mounts []Mount, opts RouterOptions) (*Router, error) {
	if opts.StartTime.IsZero() {
		opts.StartTime = time.Now()
	}
	if opts.Invalidator == nil {
		opts.Invalidator = NoopInvalidator{}
	}
	r := &Router{
		inodes:      NewInodeTable(opts.InodeCapacity),
		startTime:   opts.StartTime,
		invalidator: opts.Invalidator,
	}
	if err := r.Replace(mounts); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Router) SetInvalidator(invalidator Invalidator) {
	if invalidator == nil {
		invalidator = NoopInvalidator{}
	}
	r.invMu.Lock()
	defer r.invMu.Unlock()
	r.invalidator = invalidator
}

func (r *Router) invalidatorSnapshot() Invalidator {
	r.invMu.RLock()
	defer r.invMu.RUnlock()
	return r.invalidator
}

func (r *Router) Replace(mounts []Mount) error {
	data, err := validateMounts(mounts)
	if err != nil {
		return err
	}
	old := r.snapshot.Load()
	r.snapshot.Store(data)
	r.invalidateMountChanges(old, data)
	return nil
}

func (r *Router) Snapshot() Snapshot {
	return Snapshot{data: r.snapshot.Load()}
}

func (s Snapshot) Mounts() []Mount {
	if s.data == nil {
		return nil
	}
	result := make([]Mount, len(s.data.mounts))
	for i, entry := range s.data.mounts {
		result[i] = entry.mount
	}
	return result
}

func (r *Router) Dispatch(ctx context.Context, op Operation) (Result, error) {
	snap := r.snapshot.Load()
	if snap == nil {
		return Result{}, syscall.ENOENT
	}
	normalized, err := normalizeOperation(op)
	if err != nil {
		return Result{}, err
	}
	op = normalized

	if err := r.materializeIfNeeded(ctx, snap, op); err != nil {
		return Result{}, err
	}

	switch op.Kind {
	case OpGetAttr:
		return r.dispatchGetAttr(ctx, snap, op)
	case OpReadDir:
		return r.dispatchReadDir(ctx, snap, op)
	case OpRename:
		return r.dispatchRename(ctx, snap, op)
	default:
		res, err := r.routeRaw(ctx, snap, op)
		if err != nil {
			return Result{}, err
		}
		res = r.finalizeResult(op, res)
		r.invalidateOperation(op, res)
		return res, nil
	}
}

func validateMounts(mounts []Mount) (*snapshotData, error) {
	ids := map[string]bool{}
	entries := make([]mountEntry, 0, len(mounts))
	for i, mount := range mounts {
		if mount == nil {
			return nil, fmt.Errorf("mount %d is nil", i)
		}
		id := strings.TrimSpace(mount.ID())
		if id == "" {
			return nil, fmt.Errorf("mount %d has empty ID", i)
		}
		if ids[id] {
			return nil, fmt.Errorf("duplicate mount ID: %s", id)
		}
		ids[id] = true
		base, err := NormalizeVirtualPath(mount.BasePath())
		if err != nil {
			return nil, fmt.Errorf("mount %s base path: %w", id, err)
		}
		entries = append(entries, mountEntry{mount: mount, base: base, index: i})
	}
	return &snapshotData{mounts: entries}, nil
}

func normalizeOperation(op Operation) (Operation, error) {
	var err error
	if op.Kind == OpRename {
		op.OldPath, err = NormalizeVirtualPath(op.OldPath)
		if err != nil {
			return op, err
		}
		op.NewPath, err = NormalizeVirtualPath(op.NewPath)
		return op, err
	}
	op.Path, err = NormalizeVirtualPath(op.Path)
	return op, err
}

func (r *Router) dispatchGetAttr(ctx context.Context, snap *snapshotData, op Operation) (Result, error) {
	res, err := r.routeRaw(ctx, snap, op)
	if err == nil {
		return r.finalizeResult(op, res), nil
	}
	if !isErrno(err, syscall.ENOENT) {
		return Result{}, err
	}
	attr, ok := r.syntheticAttr(snap, op.Path)
	if !ok {
		return Result{}, err
	}
	res = Result{Attr: &attr}
	return r.finalizeResult(op, res), nil
}

func (r *Router) dispatchReadDir(ctx context.Context, snap *snapshotData, op Operation) (Result, error) {
	res, err := r.routeRaw(ctx, snap, op)
	if err != nil {
		if !isErrno(err, syscall.ENOENT) || !r.isSyntheticDir(snap, op.Path) {
			return Result{}, err
		}
		res = Result{handledIndex: -1, handledSet: true}
	}
	res.Entries = r.mergeSyntheticEntries(snap, op.Path, handledIndex(res), res.Entries)
	return r.finalizeResult(op, res), nil
}

func (r *Router) dispatchRename(ctx context.Context, snap *snapshotData, op Operation) (Result, error) {
	oldTop := topMatching(snap, op.OldPath)
	newTop := topMatching(snap, op.NewPath)
	if oldTop == nil || newTop == nil {
		return Result{}, syscall.ENOENT
	}
	if oldTop.mount.ID() != newTop.mount.ID() {
		if preparer, ok := newTop.mount.(CrossMountRenamePreparer); ok {
			prepared, err := preparer.PrepareCrossMountRename(ctx, op)
			if err != nil {
				return Result{}, err
			}
			if prepared {
				r.invalidatorSnapshot().EntryChanged(parentPath(op.NewPath), baseName(op.NewPath))
			}
		}
		return Result{}, syscall.EXDEV
	}
	res, err := r.routeRenameRaw(ctx, snap, op)
	if err != nil {
		return Result{}, err
	}
	res = r.finalizeResult(op, res)
	r.invalidateOperation(op, res)
	return res, nil
}

func (r *Router) routeRaw(ctx context.Context, snap *snapshotData, op Operation) (Result, error) {
	if op.Kind == OpRename {
		return r.routeRenameRaw(ctx, snap, op)
	}
	path := op.PrimaryPath()
	var chain []mountEntry
	for i := len(snap.mounts) - 1; i >= 0; i-- {
		entry := snap.mounts[i]
		if pathMatchesBase(entry.base, path) {
			chain = append(chain, entry)
		}
	}
	if len(chain) == 0 {
		return Result{}, syscall.ENOENT
	}
	return callChain(ctx, op, chain, 0)
}

func (r *Router) routeRenameRaw(ctx context.Context, snap *snapshotData, op Operation) (Result, error) {
	var chain []mountEntry
	for i := len(snap.mounts) - 1; i >= 0; i-- {
		entry := snap.mounts[i]
		if pathMatchesBase(entry.base, op.OldPath) && pathMatchesBase(entry.base, op.NewPath) {
			chain = append(chain, entry)
		}
	}
	if len(chain) == 0 {
		return Result{}, syscall.ENOENT
	}
	return callChain(ctx, op, chain, 0)
}

func callChain(ctx context.Context, op Operation, chain []mountEntry, pos int) (Result, error) {
	if pos >= len(chain) {
		return Result{}, syscall.ENOENT
	}
	entry := chain[pos]
	next := func(nextCtx context.Context, nextOp Operation) (Result, error) {
		return callChain(nextCtx, nextOp, chain, pos+1)
	}
	res, err := entry.mount.Handle(ctx, op, next)
	if err != nil {
		return Result{}, err
	}
	if !res.handledSet {
		res.handledIndex = entry.index
		res.handledSet = true
	}
	return res, nil
}

func topMatching(snap *snapshotData, p string) *mountEntry {
	for i := len(snap.mounts) - 1; i >= 0; i-- {
		entry := snap.mounts[i]
		if pathMatchesBase(entry.base, p) {
			return &entry
		}
	}
	return nil
}

func handledIndex(res Result) int {
	if !res.handledSet {
		return -1
	}
	return res.handledIndex
}

func (r *Router) syntheticAttr(snap *snapshotData, p string) (Attr, bool) {
	if !r.isSyntheticDir(snap, p) {
		return Attr{}, false
	}
	return Attr{
		Object: ObjectKey{Kind: "synthetic-dir", Key: p},
		Mode:   syscall.S_IFDIR | 0o777,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  2,
		ATime:  r.startTime,
		MTime:  r.startTime,
		CTime:  r.startTime,
	}, true
}

func (r *Router) isSyntheticDir(snap *snapshotData, p string) bool {
	if p == "/" {
		return false
	}
	for _, entry := range snap.mounts {
		if pathBelow(p, entry.base) {
			return true
		}
	}
	return false
}

func (r *Router) mergeSyntheticEntries(snap *snapshotData, dir string, lowerIndex int, entries []DirEntry) []DirEntry {
	seen := map[string]bool{}
	merged := append([]DirEntry(nil), entries...)
	for _, entry := range merged {
		seen[entry.Name] = true
	}
	for i := lowerIndex + 1; i < len(snap.mounts); i++ {
		mountBase := snap.mounts[i].base
		segment, ok := nextSegmentBelow(dir, mountBase)
		if !ok || seen[segment] {
			continue
		}
		childPath := joinVirtual(dir, segment)
		attr, _ := r.syntheticAttr(snap, childPath)
		merged = append(merged, DirEntry{
			Name:   segment,
			Object: attr.Object,
			Mode:   attr.Mode,
		})
		seen[segment] = true
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

func (r *Router) materializeIfNeeded(ctx context.Context, snap *snapshotData, op Operation) error {
	var target string
	switch op.Kind {
	case OpCreate, OpMkdir, OpSymlink:
		target = parentPath(op.Path)
	case OpOpen:
		if op.Flags&syscall.O_CREAT == 0 {
			return nil
		}
		target = parentPath(op.Path)
	case OpRename:
		target = parentPath(op.NewPath)
	default:
		return nil
	}
	if target == "/" {
		return nil
	}
	res, err := r.routeRaw(ctx, snap, Operation{Kind: OpGetAttr, Path: target})
	if err == nil {
		if res.Attr != nil && !res.Attr.IsDir() {
			return syscall.ENOTDIR
		}
		return nil
	}
	if !isErrno(err, syscall.ENOENT) || !r.isSyntheticDir(snap, target) {
		return nil
	}
	if _, err := r.routeRaw(ctx, snap, Operation{Kind: OpMaterialize, Path: target}); err != nil {
		return err
	}
	r.invalidatorSnapshot().EntryChanged(parentPath(target), baseName(target))
	return nil
}

func (r *Router) finalizeResult(op Operation, res Result) Result {
	if res.Attr != nil {
		r.assignAttr(op.PrimaryPath(), res.Attr)
	}
	for i := range res.Entries {
		entryPath := joinVirtual(op.PrimaryPath(), res.Entries[i].Name)
		if res.Entries[i].Object.empty() {
			res.Entries[i].Object = ObjectKey{Kind: "dir-entry", Key: entryPath}
		}
		res.Entries[i].Inode = r.inodes.Access(res.Entries[i].Object)
	}
	if res.Handle != nil && res.Attr != nil && !res.Attr.Object.empty() {
		pin := r.inodes.Pin(res.Attr.Object)
		res.Attr.Inode = pin.Inode
		res.Handle = &routedHandle{inner: res.Handle, pin: pin, invalidator: r.invalidatorSnapshot(), key: res.Attr.Object}
	}
	return res
}

func (r *Router) assignAttr(p string, attr *Attr) {
	if attr.Object.empty() {
		attr.Object = ObjectKey{Kind: "path", Key: p}
	}
	attr.Inode = r.inodes.Access(attr.Object)
}

func (r *Router) invalidateOperation(op Operation, res Result) {
	invalidator := r.invalidatorSnapshot()
	switch op.Kind {
	case OpOpen:
		if op.Flags&syscall.O_CREAT != 0 {
			invalidator.EntryChanged(parentPath(op.Path), baseName(op.Path))
		}
	case OpCreate, OpMkdir, OpSymlink:
		invalidator.EntryChanged(parentPath(op.Path), baseName(op.Path))
	case OpUnlink, OpRmdir:
		invalidator.EntryChanged(parentPath(op.Path), baseName(op.Path))
	case OpRename:
		invalidator.EntryChanged(parentPath(op.OldPath), baseName(op.OldPath))
		invalidator.EntryChanged(parentPath(op.NewPath), baseName(op.NewPath))
	case OpSetAttr:
		if res.Attr != nil {
			invalidator.InodeChanged(res.Attr.Object)
		}
	}
}

func (r *Router) invalidateMountChanges(old, new *snapshotData) {
	invalidator := r.invalidatorSnapshot()
	seen := map[string]bool{}
	collect := func(data *snapshotData) {
		if data == nil {
			return
		}
		for _, entry := range data.mounts {
			if entry.base == "/" || seen[entry.base] {
				continue
			}
			seen[entry.base] = true
			invalidator.EntryChanged(parentPath(entry.base), baseName(entry.base))
		}
	}
	collect(old)
	collect(new)
}

type routedHandle struct {
	inner       FileHandle
	pin         InodePin
	invalidator Invalidator
	key         ObjectKey
	dirty       atomic.Bool
}

func (h *routedHandle) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	reader, ok := h.inner.(FileReader)
	if !ok {
		return nil, syscall.ENOSYS
	}
	return reader.Read(ctx, dest, off)
}

func (h *routedHandle) Write(ctx context.Context, data []byte, off int64) (uint32, error) {
	writer, ok := h.inner.(FileWriter)
	if !ok {
		return 0, syscall.ENOSYS
	}
	written, err := writer.Write(ctx, data, off)
	if err == nil {
		h.dirty.Store(true)
		h.invalidator.InodeChanged(h.key)
	}
	return written, err
}

func (h *routedHandle) Flush(ctx context.Context) error {
	flusher, ok := h.inner.(FileFlusher)
	if !ok {
		return nil
	}
	err := flusher.Flush(ctx)
	if err == nil && h.dirty.Swap(false) {
		h.invalidator.InodeChanged(h.key)
	}
	return err
}

func (h *routedHandle) Fsync(ctx context.Context, flags uint32) error {
	fsyncer, ok := h.inner.(FileFsyncer)
	if !ok {
		return nil
	}
	return fsyncer.Fsync(ctx, flags)
}

func (h *routedHandle) Release(ctx context.Context) error {
	defer h.pin.Unpin()
	releaser, ok := h.inner.(FileReleaser)
	if !ok {
		return nil
	}
	return releaser.Release(ctx)
}

func (h *routedHandle) GetAttr(ctx context.Context) (Attr, error) {
	getter, ok := h.inner.(FileGetattrer)
	if !ok {
		return Attr{}, syscall.ENOSYS
	}
	return getter.GetAttr(ctx)
}

func (h *routedHandle) SetAttr(ctx context.Context, attr SetAttr) (Attr, error) {
	setter, ok := h.inner.(FileSetattrer)
	if !ok {
		return Attr{}, syscall.ENOSYS
	}
	result, err := setter.SetAttr(ctx, attr)
	if err == nil {
		h.dirty.Store(true)
		h.invalidator.InodeChanged(h.key)
	}
	return result, err
}
