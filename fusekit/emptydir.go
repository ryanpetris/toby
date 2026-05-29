package fusekit

import (
	"context"
	"os"
	"syscall"
	"time"
)

type EmptyDirMount struct {
	id        string
	basePath  string
	mode      uint32
	createdAt time.Time
}

func NewEmptyDirMount(id, basePath string, mode uint32) (*EmptyDirMount, error) {
	base, err := NormalizeVirtualPath(basePath)
	if err != nil {
		return nil, err
	}
	if mode == 0 {
		mode = 0o777
	}
	return &EmptyDirMount{id: id, basePath: base, mode: mode, createdAt: time.Now()}, nil
}

func (m *EmptyDirMount) ID() string { return m.id }

func (m *EmptyDirMount) BasePath() string { return m.basePath }

func (m *EmptyDirMount) Handle(ctx context.Context, op Operation, next Next) (Result, error) {
	switch op.Kind {
	case OpGetAttr:
		if op.Path != m.basePath {
			return next(ctx, op)
		}
		attr := m.attr()
		return Result{Attr: &attr}, nil
	case OpReadDir:
		if op.Path != m.basePath {
			return next(ctx, op)
		}
		return Result{Entries: nil}, nil
	case OpCreate, OpMkdir, OpUnlink, OpRmdir, OpRename, OpSymlink, OpSetAttr, OpMaterialize:
		return Result{}, syscall.EROFS
	default:
		return next(ctx, op)
	}
}

func (m *EmptyDirMount) attr() Attr {
	return Attr{
		Object: ObjectKey{MountID: m.id, Kind: "empty-dir", Key: "/"},
		Mode:   syscall.S_IFDIR | m.mode,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  2,
		ATime:  m.createdAt,
		MTime:  m.createdAt,
		CTime:  m.createdAt,
	}
}
