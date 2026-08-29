package approvals

// The approvals.* method handlers and the approve-time executor that
// re-dispatches a held request through the launch's host-action router.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/hostaction"
)

const (
	defaultWaitTimeout = 5 * time.Minute
	maxWaitTimeout     = 30 * time.Minute
)

// Dispatcher re-enters the launch's host-action router with a held request.
type Dispatcher func(context.Context, hostaction.RPCRequest) ([]byte, error)

var _ hostaction.Capability = (*Service)(nil)

// Service handles the approvals.* methods. Bind installs the launch's registry
// and router dispatch once they exist; Close joins in-flight executions and is
// required before the registry's final DenyAll.
type Service struct {
	mu         sync.Mutex
	registry   *approval.Registry
	dispatch   Dispatcher
	cancel     context.CancelFunc
	executions sync.WaitGroup

	// executorCtx is the component-lifetime context owned by this service; it
	// bounds approve-time executions and is canceled by Close.
	executorCtx context.Context

	logger *diagnostic.Logger
}

// New creates the approvals capability; call Bind before routing to it.
func New(diagnostics *diagnostic.Service) *Service {
	return &Service{logger: diagnostics.Logger("hostaction.approvals")}
}

// Bind installs the launch's approval registry and router dispatch.
func (s *Service) Bind(registry *approval.Registry, dispatch Dispatcher) {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.registry = registry
	s.dispatch = dispatch
	s.executorCtx = ctx
	s.cancel = cancel
	s.mu.Unlock()
}

// Close cancels and joins in-flight approved executions and detaches the
// registry. The caller then denies the remaining records.
func (s *Service) Close() {
	s.mu.Lock()
	cancel := s.cancel
	s.registry = nil
	s.dispatch = nil
	s.executorCtx = nil
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.executions.Wait()
}

// Methods registers the approvals.* handlers into the host router.
func (s *Service) Methods() []hostaction.Method {
	return []hostaction.Method{
		{Name: MethodList, Handle: s.handleList},
		{Name: MethodDecide, Handle: s.handleDecide},
		{Name: MethodWait, Handle: s.handleWait},
	}
}

func (s *Service) handleList(_ context.Context, req hostaction.RPCRequest) ([]byte, error) {
	registry, _, _ := s.current()
	if registry == nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInternalError, "approvals capability is not bound", nil), syscall.ENOSYS
	}

	pending := registry.Pending()
	result := ListResult{Approvals: make([]PendingApproval, 0, len(pending))}
	for _, record := range pending {
		result.Approvals = append(result.Approvals, PendingApproval{
			ApprovalID:    record.ID,
			Action:        record.Action,
			Name:          record.Name,
			Message:       record.Message,
			CreatedUnixMS: record.Created.UnixMilli(),
		})
	}

	return hostaction.ResponseOK(req.ID, result), nil
}

func (s *Service) handleDecide(_ context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeDecideParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	registry, _, _ := s.current()
	if registry == nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInternalError, "approvals capability is not bound", nil), syscall.ENOSYS
	}

	var record approval.Record
	var status approval.Status
	var changed bool
	if params.Approve {
		record, status, changed, err = registry.Approve(params.ApprovalID)
	} else {
		record, status, changed, err = registry.Deny(params.ApprovalID)
	}
	if errors.Is(err, approval.ErrNotFound) {
		return hostaction.ResponseError(req.ID, hostaction.CodeApprovalNotFound, err.Error(), nil), syscall.ENOENT
	}
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInternalError, err.Error(), nil), syscall.EIO
	}
	if params.Approve && changed {
		s.startExecution(record)
	}

	return hostaction.ResponseOK(req.ID, DecideResult{
		ApprovalID: record.ID,
		Status:     wireStatus(status),
		Changed:    changed,
		Name:       record.Name,
		Message:    record.Message,
	}), nil
}

func (s *Service) handleWait(ctx context.Context, req hostaction.RPCRequest) ([]byte, error) {
	params, err := DecodeWaitParams(req.Params)
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInvalidParams, err.Error(), nil), syscall.EINVAL
	}
	registry, _, _ := s.current()
	if registry == nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInternalError, "approvals capability is not bound", nil), syscall.ENOSYS
	}

	timeout := defaultWaitTimeout
	if params.TimeoutMS > 0 {
		timeout = min(
			time.Duration(params.TimeoutMS)*time.Millisecond,
			maxWaitTimeout,
		)
	}
	record, status, response, err := registry.Wait(ctx, params.ApprovalID, timeout)
	if errors.Is(err, approval.ErrNotFound) {
		return hostaction.ResponseError(req.ID, hostaction.CodeApprovalNotFound, err.Error(), nil), syscall.ENOENT
	}
	if err != nil {
		return hostaction.ResponseError(req.ID, hostaction.CodeInternalError, err.Error(), nil), err
	}

	return hostaction.ResponseOK(req.ID, WaitResult{
		ApprovalID: params.ApprovalID,
		Status:     wireStatus(status),
		Action:     record.Action,
		Response:   response,
	}), nil
}

// startExecution runs one approved record's held request through the router
// under its one-shot grant and saves the response for waiting callers. The
// decide reply does not wait for it.
func (s *Service) startExecution(record approval.Record) {
	registry, dispatch, executorCtx := s.current()

	s.executions.Add(1)
	go func() {
		defer s.executions.Done()

		response := s.dispatchHeld(
			registry,
			dispatch,
			executorCtx,
			record,
		)
		saveErr := error(nil)
		if registry != nil {
			saveErr = registry.SaveResponse(record.ID, response)
		}
		s.logger.Debug(
			"executed approved host action",
			"approval", record.ID,
			"action", record.Action,
			"save_error", saveErr,
		)
	}()
}

func (s *Service) dispatchHeld(
	registry *approval.Registry,
	dispatch Dispatcher,
	executorCtx context.Context,
	record approval.Record,
) []byte {
	if registry == nil || dispatch == nil || executorCtx == nil {
		return hostaction.ResponseError(
			record.RequestID,
			hostaction.CodeInternalError,
			"approvals capability is not bound",
			nil,
		)
	}

	ctx := approval.WithGrant(executorCtx, record.ID)
	response, err := dispatch(ctx, hostaction.RPCRequest{
		JSONRPC: hostaction.JSONRPCVersion,
		ID:      record.RequestID,
		Method:  record.Action,
		Params:  record.Params,
	})
	s.logger.Debug(
		"dispatched approved host action",
		"approval", record.ID,
		"action", record.Action,
		"dispatch_error", err,
	)
	if len(response) == 0 {
		message := "approved action produced no response"
		if err != nil {
			message = fmt.Sprintf("approved action failed: %v", err)
		}
		return hostaction.ResponseError(
			record.RequestID,
			hostaction.CodeInternalError,
			message,
			nil,
		)
	}
	return response
}

func (s *Service) current() (*approval.Registry, Dispatcher, context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registry, s.dispatch, s.executorCtx
}

func wireStatus(status approval.Status) string {
	switch status {
	case approval.StatusExecuting, approval.StatusCompleted:
		return StatusApproved
	case approval.StatusDenied:
		return StatusDenied
	default:
		return StatusPending
	}
}
