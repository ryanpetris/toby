package fusekit

import "context"

type Next func(context.Context, Operation) (Result, error)

type Mount interface {
	ID() string
	BasePath() string
	Handle(context.Context, Operation, Next) (Result, error)
}

type CrossMountRenamePreparer interface {
	PrepareCrossMountRename(context.Context, Operation) (bool, error)
}

type OperationHandler func(context.Context, Operation, Next) (Result, error)

type MountAdapter struct {
	IDValue       string
	BasePathValue string
	Handler       OperationHandler
	GetAttr       OperationHandler
	ReadDir       OperationHandler
	Open          OperationHandler
	Create        OperationHandler
	Mkdir         OperationHandler
	Unlink        OperationHandler
	Rmdir         OperationHandler
	Rename        OperationHandler
	Readlink      OperationHandler
	Symlink       OperationHandler
	SetAttr       OperationHandler
	Materialize   OperationHandler
}

func (m MountAdapter) ID() string { return m.IDValue }

func (m MountAdapter) BasePath() string { return m.BasePathValue }

func (m MountAdapter) Handle(ctx context.Context, op Operation, next Next) (Result, error) {
	if handler := m.handler(op.Kind); handler != nil {
		return handler(ctx, op, next)
	}
	if m.Handler != nil {
		return m.Handler(ctx, op, next)
	}
	return next(ctx, op)
}

func (m MountAdapter) handler(kind OperationKind) OperationHandler {
	switch kind {
	case OpGetAttr:
		return m.GetAttr
	case OpReadDir:
		return m.ReadDir
	case OpOpen:
		return m.Open
	case OpCreate:
		return m.Create
	case OpMkdir:
		return m.Mkdir
	case OpUnlink:
		return m.Unlink
	case OpRmdir:
		return m.Rmdir
	case OpRename:
		return m.Rename
	case OpReadlink:
		return m.Readlink
	case OpSymlink:
		return m.Symlink
	case OpSetAttr:
		return m.SetAttr
	case OpMaterialize:
		return m.Materialize
	default:
		return nil
	}
}
