package client

// Executes typed resource-list, model, and disk-log agent operations.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// ListModels returns one item response per model for an active models lease.
func (s *AgentSession) ListModels(
	ctx context.Context,
	lease *ResourceLease,
) ([]protocol.ModelsListItemResponse, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return nil, err
	}
	if lease == nil || lease.session != s {
		return nil, fmt.Errorf(
			"models resource lease does not belong to this agent session",
		)
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	stream, err := s.client.ListModels(
		ctx,
		&agentv1.ModelsListRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			LeaseId:       string(lease.leaseID),
		},
	)
	if err != nil {
		return nil, remoteRequestError(err, id)
	}

	var operationID protocol.OperationID
	var sequence uint64
	var result []protocol.ModelsListItemResponse
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf(
				"models listing ended without a terminal result",
			)
		}
		if err != nil {
			return nil, remoteRequestError(err, id)
		}
		if err := requireCorrelation(
			event.GetCorrelationId(),
			id,
		); err != nil {
			return nil, err
		}

		switch {
		case event.GetAccepted() != nil:
			if operationID != "" {
				return nil, fmt.Errorf("models listing was accepted twice")
			}
			operationID = protocol.OperationID(
				event.GetAccepted().GetOperationId(),
			)
			if err := protocol.ValidateOperationID(operationID); err != nil {
				return nil, fmt.Errorf(
					"models listing returned an invalid operation ID",
				)
			}
		case event.GetItem() != nil:
			item := event.GetItem()
			if err := validateStreamItem(
				operationID,
				&sequence,
				protocol.OperationID(item.GetOperationId()),
				item.GetSequence(),
			); err != nil {
				return nil, err
			}
			if err := protocol.ValidateModelID(
				item.GetModelId(),
			); err != nil {
				return nil, fmt.Errorf(
					"models listing returned an invalid model ID",
				)
			}
			if len(item.GetModel()) == 0 ||
				len(item.GetModel()) > protocol.MaxConfigurationBytes ||
				!json.Valid(item.GetModel()) {
				return nil, fmt.Errorf(
					"models listing returned invalid model metadata",
				)
			}
			result = append(result, protocol.ModelsListItemResponse{
				CorrelationID: id,
				OperationID:   operationID,
				Sequence:      sequence,
				ModelID:       item.GetModelId(),
				Model: append(
					[]byte(nil),
					item.GetModel()...,
				),
			})
		case event.GetComplete() != nil:
			complete := event.GetComplete()
			if err := validateStreamItem(
				operationID,
				&sequence,
				protocol.OperationID(complete.GetOperationId()),
				complete.GetSequence(),
			); err != nil {
				return nil, err
			}
			return result, nil
		case event.GetFailed() != nil:
			failed := event.GetFailed()
			if err := validateStreamItem(
				operationID,
				&sequence,
				protocol.OperationID(failed.GetOperationId()),
				failed.GetSequence(),
			); err != nil {
				return nil, err
			}
			return nil, errors.New(failed.GetMessage())
		default:
			return nil, fmt.Errorf("models listing returned an empty event")
		}
	}
}

// FlushModelsCache invalidates one active models resource cache.
func (s *AgentSession) FlushModelsCache(
	ctx context.Context,
	lease *ResourceLease,
) error {
	if err := s.validateRequestContext(ctx); err != nil {
		return err
	}
	if lease == nil || lease.session != s {
		return fmt.Errorf(
			"models resource lease does not belong to this agent session",
		)
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return err
	}
	response, err := s.client.FlushModelsCache(
		ctx,
		&agentv1.ModelsCacheFlushRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			LeaseId:       string(lease.leaseID),
		},
	)
	if err != nil {
		return remoteRequestError(err, id)
	}

	return requireCorrelation(response.GetCorrelationId(), id)
}

// Resources lists active agent resources without configuration.
func (s *AgentSession) Resources(
	ctx context.Context,
) ([]protocol.ResourceListItem, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return nil, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	stream, err := s.client.ListResources(
		ctx,
		&agentv1.ResourceListRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
		},
	)
	if err != nil {
		return nil, remoteRequestError(err, id)
	}

	var sequence uint64
	var result []protocol.ResourceListItem
	for {
		item, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, remoteRequestError(err, id)
		}
		if err := requireCorrelation(item.GetCorrelationId(), id); err != nil {
			return nil, err
		}
		if item.GetSequence() != sequence+1 {
			return nil, fmt.Errorf(
				"resource-list sequence is not contiguous",
			)
		}
		sequence = item.GetSequence()

		kind, err := protocol.ResourceKindFromAgent(item.GetKind())
		if err != nil {
			return nil, err
		}
		resourceID := protocol.ResourceID(item.GetResourceId())
		if err := protocol.ValidateResourceID(resourceID); err != nil {
			return nil, fmt.Errorf(
				"resource list returned an invalid resource ID",
			)
		}
		result = append(result, protocol.ResourceListItem{
			CorrelationID: id,
			Sequence:      sequence,
			ResourceID:    resourceID,
			Kind:          kind,
			ActiveLeases:  item.GetActiveLeases(),
		})
	}
}

// ReadResourceLog copies one requested or latest retained log verbatim.
func (s *AgentSession) ReadResourceLog(
	ctx context.Context,
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	operationID protocol.OperationID,
	destination io.Writer,
) (protocol.OperationID, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return "", err
	}
	if destination == nil {
		return "", fmt.Errorf("resource log destination is nil")
	}
	if err := kind.Validate(); err != nil {
		return "", err
	}
	if err := protocol.ValidateResourceID(resourceID); err != nil {
		return "", err
	}
	if operationID != "" {
		if err := protocol.ValidateOperationID(operationID); err != nil {
			return "", err
		}
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return "", err
	}
	stream, err := s.client.ReadResourceLog(
		ctx,
		&agentv1.ResourceLogRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			Kind:          protocol.ResourceKindToAgent(kind),
			ResourceId:    string(resourceID),
			OperationId:   string(operationID),
		},
	)
	if err != nil {
		return "", remoteRequestError(err, id)
	}

	var selected protocol.OperationID
	var sequence uint64
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf(
				"resource log ended without a terminal result",
			)
		}
		if err != nil {
			return "", remoteRequestError(err, id)
		}
		if err := requireCorrelation(
			event.GetCorrelationId(),
			id,
		); err != nil {
			return "", err
		}

		switch {
		case event.GetAccepted() != nil:
			if selected != "" {
				return "", fmt.Errorf(
					"resource log read was accepted twice",
				)
			}
			selected = protocol.OperationID(
				event.GetAccepted().GetOperationId(),
			)
			if err := protocol.ValidateOperationID(selected); err != nil {
				return "", fmt.Errorf(
					"resource log returned an invalid operation ID",
				)
			}
		case event.GetChunk() != nil:
			chunk := event.GetChunk()
			if err := validateStreamItem(
				selected,
				&sequence,
				protocol.OperationID(chunk.GetOperationId()),
				chunk.GetSequence(),
			); err != nil {
				return "", err
			}
			if len(chunk.GetData()) == 0 {
				return "", fmt.Errorf(
					"resource log returned an empty chunk",
				)
			}
			if _, err := destination.Write(chunk.GetData()); err != nil {
				return "", err
			}
		case event.GetComplete() != nil:
			complete := event.GetComplete()
			if err := validateStreamItem(
				selected,
				&sequence,
				protocol.OperationID(complete.GetOperationId()),
				complete.GetSequence(),
			); err != nil {
				return "", err
			}
			return selected, nil
		case event.GetFailed() != nil:
			failed := event.GetFailed()
			if err := validateStreamItem(
				selected,
				&sequence,
				protocol.OperationID(failed.GetOperationId()),
				failed.GetSequence(),
			); err != nil {
				return "", err
			}
			return "", errors.New(failed.GetMessage())
		default:
			return "", fmt.Errorf("resource log returned an empty event")
		}
	}
}

func validateStreamItem(
	accepted protocol.OperationID,
	sequence *uint64,
	operationID protocol.OperationID,
	actual uint64,
) error {
	if accepted == "" || operationID != accepted {
		return fmt.Errorf("stream operation identity is invalid")
	}
	if actual != *sequence+1 {
		return fmt.Errorf("stream operation sequence is not contiguous")
	}
	*sequence = actual

	return nil
}
