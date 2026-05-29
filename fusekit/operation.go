package fusekit

import (
	"context"
	"syscall"
	"time"
)

type OperationKind string

const (
	OpGetAttr     OperationKind = "getattr"
	OpReadDir     OperationKind = "readdir"
	OpOpen        OperationKind = "open"
	OpCreate      OperationKind = "create"
	OpMkdir       OperationKind = "mkdir"
	OpUnlink      OperationKind = "unlink"
	OpRmdir       OperationKind = "rmdir"
	OpRename      OperationKind = "rename"
	OpReadlink    OperationKind = "readlink"
	OpSymlink     OperationKind = "symlink"
	OpSetAttr     OperationKind = "setattr"
	OpMaterialize OperationKind = "materialize"
)

type Operation struct {
	Kind    OperationKind
	Path    string
	OldPath string
	NewPath string
	Flags   uint32
	Mode    uint32
	Target  string
	SetAttr SetAttr
}

func (op Operation) PrimaryPath() string {
	if op.Kind == OpRename {
		return op.OldPath
	}
	return op.Path
}

func (op Operation) DestinationPath() string {
	if op.Kind == OpRename {
		return op.NewPath
	}
	return op.Path
}

type SetAttr struct {
	Mode  *uint32
	UID   *uint32
	GID   *uint32
	Size  *uint64
	ATime *time.Time
	MTime *time.Time
}

type Result struct {
	Attr      *Attr
	Entries   []DirEntry
	Handle    FileHandle
	Data      []byte
	FuseFlags uint32

	handledIndex int
	handledSet   bool
}

type Attr struct {
	Object  ObjectKey
	Inode   uint64
	Mode    uint32
	Size    uint64
	UID     uint32
	GID     uint32
	Nlink   uint32
	Rdev    uint32
	Blocks  uint64
	Blksize uint32
	ATime   time.Time
	MTime   time.Time
	CTime   time.Time
}

func (a Attr) IsDir() bool {
	return a.Mode&syscall.S_IFMT == syscall.S_IFDIR
}

type DirEntry struct {
	Name   string
	Object ObjectKey
	Inode  uint64
	Mode   uint32
}

type FileHandle interface{}

type FileReader interface {
	Read(context.Context, []byte, int64) ([]byte, error)
}

type FileWriter interface {
	Write(context.Context, []byte, int64) (uint32, error)
}

type FileFlusher interface {
	Flush(context.Context) error
}

type FileFsyncer interface {
	Fsync(context.Context, uint32) error
}

type FileReleaser interface {
	Release(context.Context) error
}

type FileGetattrer interface {
	GetAttr(context.Context) (Attr, error)
}

type FileSetattrer interface {
	SetAttr(context.Context, SetAttr) (Attr, error)
}
