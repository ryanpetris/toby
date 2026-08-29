package server

// Implements approval listing and decision RPCs by fanning reverse
// approvals.* host actions out to the connected launch sessions, which own
// the approval records.

import (
	"context"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/approvals"
)

// approvalSessionLimit bounds each launch session's reverse approvals call so
// one hung launch cannot stall the whole listing or decision.
const approvalSessionLimit = 5 * time.Second

// ListApprovals aggregates the pending approvals of every connected launch.
func (s *Service) ListApprovals(
	ctx context.Context,
	request *agentv1.ApprovalListRequest,
) (*agentv1.ApprovalListResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "approval list request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	response := &agentv1.ApprovalListResponse{
		CorrelationId: request.GetCorrelationId(),
	}
	for _, current := range s.sessionsSnapshot() {
		if current.id == session.id {
			continue
		}
		listing, callErr := callSessionApprovals(
			ctx,
			current,
			approvals.NewListRequest,
		)
		if callErr != nil {
			response.UnreachableSessions++
			continue
		}
		if listing.Error != nil {
			response.UnreachableSessions++
			continue
		}
		result, decodeErr := approvals.DecodeListResult(listing.Result)
		if decodeErr != nil {
			response.UnreachableSessions++
			continue
		}
		for _, pending := range result.Approvals {
			response.Approvals = append(
				response.Approvals,
				&agentv1.PendingApproval{
					ApprovalId:              pending.ApprovalID,
					Action:                  pending.Action,
					Name:                    pending.Name,
					Message:                 pending.Message,
					CreatedUnixMilliseconds: pending.CreatedUnixMS,
					LaunchSessionId:         string(current.id),
				},
			)
		}
	}

	return response, nil
}

// DecideApproval records one decision on the launch session that owns the
// approval id. Launches that do not know the id are skipped.
func (s *Service) DecideApproval(
	ctx context.Context,
	request *agentv1.ApprovalDecideRequest,
) (*agentv1.ApprovalDecideResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "approval decide request is required")
	}
	if request.GetApprovalId() == "" {
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"approval id is required",
		)
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	for _, current := range s.sessionsSnapshot() {
		if current.id == session.id {
			continue
		}
		decision, callErr := callSessionApprovals(
			ctx,
			current,
			func(id int64) ([]byte, error) {
				return approvals.NewDecideRequest(id, approvals.DecideParams{
					ApprovalID: request.GetApprovalId(),
					Approve:    request.GetApprove(),
				})
			},
		)
		if callErr != nil {
			continue
		}
		if decision.Error != nil {
			if decision.Error.Code == hostaction.CodeApprovalNotFound {
				continue
			}
			return nil, agentError(
				request.GetCorrelationId(),
				protocol.ErrorInternal,
				decision.Error.Message,
				false,
			)
		}
		result, decodeErr := approvals.DecodeDecideResult(decision.Result)
		if decodeErr != nil {
			return nil, agentError(
				request.GetCorrelationId(),
				protocol.ErrorInternal,
				"parse approval decision result: "+decodeErr.Error(),
				false,
			)
		}

		return &agentv1.ApprovalDecideResponse{
			CorrelationId: request.GetCorrelationId(),
			Approval: &agentv1.PendingApproval{
				ApprovalId:      result.ApprovalID,
				Name:            result.Name,
				Message:         result.Message,
				LaunchSessionId: string(current.id),
			},
			Status:  result.Status,
			Changed: result.Changed,
		}, nil
	}

	return nil, agentError(
		request.GetCorrelationId(),
		protocol.ErrorLeaseNotFound,
		"no connected launch holds approval "+request.GetApprovalId(),
		false,
	)
}

func callSessionApprovals(
	ctx context.Context,
	session *agentSession,
	build func(int64) ([]byte, error),
) (hostaction.RPCResponse, error) {
	payload, err := build(1)
	if err != nil {
		return hostaction.RPCResponse{}, err
	}

	callCtx, cancel := boundedServerContext(ctx, approvalSessionLimit)
	defer cancel()
	raw, err := session.caller.Call(callCtx, payload)
	if err != nil {
		return hostaction.RPCResponse{}, err
	}

	return hostaction.DecodeResponse(raw)
}
