package client

// Approval listing and decision requests against the agent service.

import (
	"context"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// Approvals lists the pending approvals of every connected launch.
func (s *AgentSession) Approvals(
	ctx context.Context,
) (protocol.ApprovalListResult, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return protocol.ApprovalListResult{}, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return protocol.ApprovalListResult{}, err
	}
	requestCtx, cancel := boundedContext(ctx, s.options.RequestTimeout)
	defer cancel()

	response, err := s.client.ListApprovals(
		requestCtx,
		&agentv1.ApprovalListRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
		},
	)
	if err != nil {
		return protocol.ApprovalListResult{}, remoteRequestError(err, id)
	}
	if err := requireCorrelation(response.GetCorrelationId(), id); err != nil {
		return protocol.ApprovalListResult{}, err
	}

	result := protocol.ApprovalListResult{
		UnreachableSessions: response.GetUnreachableSessions(),
	}
	for _, pending := range response.GetApprovals() {
		result.Approvals = append(
			result.Approvals,
			pendingApprovalFromAgent(pending),
		)
	}
	return result, nil
}

// DecideApproval records one approval decision on its owning launch.
func (s *AgentSession) DecideApproval(
	ctx context.Context,
	approvalID string,
	approve bool,
) (protocol.ApprovalDecision, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return protocol.ApprovalDecision{}, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return protocol.ApprovalDecision{}, err
	}
	requestCtx, cancel := boundedContext(ctx, s.options.RequestTimeout)
	defer cancel()

	response, err := s.client.DecideApproval(
		requestCtx,
		&agentv1.ApprovalDecideRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			ApprovalId:    approvalID,
			Approve:       approve,
		},
	)
	if err != nil {
		return protocol.ApprovalDecision{}, remoteRequestError(err, id)
	}
	if err := requireCorrelation(response.GetCorrelationId(), id); err != nil {
		return protocol.ApprovalDecision{}, err
	}

	return protocol.ApprovalDecision{
		Approval: pendingApprovalFromAgent(response.GetApproval()),
		Status:   response.GetStatus(),
		Changed:  response.GetChanged(),
	}, nil
}

func pendingApprovalFromAgent(
	pending *agentv1.PendingApproval,
) protocol.PendingApproval {
	if pending == nil {
		return protocol.PendingApproval{}
	}
	return protocol.PendingApproval{
		ApprovalID:      pending.GetApprovalId(),
		Action:          pending.GetAction(),
		Name:            pending.GetName(),
		Message:         pending.GetMessage(),
		Created:         time.UnixMilli(pending.GetCreatedUnixMilliseconds()),
		LaunchSessionID: pending.GetLaunchSessionId(),
	}
}
