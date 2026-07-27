package server

// Implements typed unary and server-streaming agent resource operations.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// ListResources streams active non-secret resource entries.
func (s *Service) ListResources(
	request *agentv1.ResourceListRequest,
	stream agentv1.AgentService_ListResourcesServer,
) error {
	if request == nil {
		return invalidRequest("", "resource list request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return err
	}
	defer session.finish(correlationID)
	s.beginStream()
	defer s.finishStream()

	lister, ok := s.resourceCoordinator.(ResourceLister)
	if !ok {
		return agentError(
			request.GetCorrelationId(),
			protocol.ErrorUnavailable,
			"resource listing is unavailable",
			false,
		)
	}

	for index, item := range lister.ResourceItems() {
		if err := stream.Send(&agentv1.ResourceListItem{
			CorrelationId: request.GetCorrelationId(),
			Sequence:      uint64(index + 1),
			ResourceId:    string(item.ID),
			Kind:          protocol.ResourceKindToAgent(item.Kind),
			ActiveLeases:  item.ActiveLeases,
		}); err != nil {
			return err
		}
	}

	return nil
}

// ReadResourceLog streams one requested or latest retained operation log.
func (s *Service) ReadResourceLog(
	request *agentv1.ResourceLogRequest,
	stream agentv1.AgentService_ReadResourceLogServer,
) error {
	if request == nil {
		return invalidRequest("", "resource log request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return err
	}
	defer session.finish(correlationID)
	s.beginStream()
	defer s.finishStream()

	kind, err := protocol.ResourceKindFromAgent(request.GetKind())
	if err != nil {
		return invalidRequest(
			request.GetCorrelationId(),
			"resource log kind is invalid",
		)
	}
	resourceID := protocol.ResourceID(request.GetResourceId())
	if err := protocol.ValidateResourceID(resourceID); err != nil {
		return invalidRequest(
			request.GetCorrelationId(),
			"resource log resource ID is invalid",
		)
	}
	requestedOperation := protocol.OperationID(request.GetOperationId())
	if requestedOperation != "" {
		if err := protocol.ValidateOperationID(requestedOperation); err != nil {
			return invalidRequest(
				request.GetCorrelationId(),
				"resource log operation ID is invalid",
			)
		}
	}

	operationID := requestedOperation
	if operationID == "" {
		operationID = protocol.NewOperationID()
	}
	if s.options.ResourceLogs == nil {
		return sendLogFailure(
			stream,
			request.GetCorrelationId(),
			operationID,
		)
	}

	file, selected, err := s.options.ResourceLogs.Open(
		kind,
		resourceID,
		requestedOperation,
	)
	if err != nil {
		return sendLogFailure(
			stream,
			request.GetCorrelationId(),
			operationID,
		)
	}
	defer func() {
		s.options.Logger.DebugError(
			"close resource log after read",
			file.Close(),
			"resource_id",
			resourceID,
			"operation_id",
			operationID,
		)
	}()
	operationID = selected

	if err := stream.Send(&agentv1.ResourceLogEvent{
		CorrelationId: request.GetCorrelationId(),
		Value: &agentv1.ResourceLogEvent_Accepted{
			Accepted: &agentv1.ResourceLogAccepted{
				OperationId: string(operationID),
			},
		},
	}); err != nil {
		return err
	}

	buffer := make([]byte, protocol.MaxProgressOutputBytes)
	var sequence uint64
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			sequence++
			if err := stream.Send(&agentv1.ResourceLogEvent{
				CorrelationId: request.GetCorrelationId(),
				Value: &agentv1.ResourceLogEvent_Chunk{
					Chunk: &agentv1.ResourceLogChunk{
						OperationId: string(operationID),
						Sequence:    sequence,
						Data: append(
							[]byte(nil),
							buffer[:count]...,
						),
					},
				},
			}); err != nil {
				return err
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return stream.Send(&agentv1.ResourceLogEvent{
				CorrelationId: request.GetCorrelationId(),
				Value: &agentv1.ResourceLogEvent_Failed{
					Failed: &agentv1.ResourceLogFailed{
						OperationId: string(operationID),
						Sequence:    sequence + 1,
						Message:     "resource log read failed",
					},
				},
			})
		}
		break
	}

	return stream.Send(&agentv1.ResourceLogEvent{
		CorrelationId: request.GetCorrelationId(),
		Value: &agentv1.ResourceLogEvent_Complete{
			Complete: &agentv1.ResourceLogComplete{
				OperationId: string(operationID),
				Sequence:    sequence + 1,
			},
		},
	})
}

func sendLogFailure(
	stream agentv1.AgentService_ReadResourceLogServer,
	correlationID string,
	operationID protocol.OperationID,
) error {
	if err := stream.Send(&agentv1.ResourceLogEvent{
		CorrelationId: correlationID,
		Value: &agentv1.ResourceLogEvent_Accepted{
			Accepted: &agentv1.ResourceLogAccepted{
				OperationId: string(operationID),
			},
		},
	}); err != nil {
		return err
	}

	return stream.Send(&agentv1.ResourceLogEvent{
		CorrelationId: correlationID,
		Value: &agentv1.ResourceLogEvent_Failed{
			Failed: &agentv1.ResourceLogFailed{
				OperationId: string(operationID),
				Sequence:    1,
				Message:     "resource log is unavailable",
			},
		},
	})
}

// ListModels streams discovered metadata for one active models lease.
func (s *Service) ListModels(
	request *agentv1.ModelsListRequest,
	stream agentv1.AgentService_ListModelsServer,
) error {
	if request == nil {
		return invalidRequest("", "models list request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return err
	}
	defer session.finish(correlationID)
	s.beginStream()
	defer s.finishStream()

	leaseID := protocol.LeaseID(request.GetLeaseId())
	if err := protocol.ValidateLeaseID(leaseID); err != nil {
		return invalidRequest(
			request.GetCorrelationId(),
			"models lease ID is invalid",
		)
	}
	if !session.ownsLeaseID(leaseID) {
		return agentError(
			request.GetCorrelationId(),
			protocol.ErrorLeaseNotFound,
			"models resource lease is unavailable",
			false,
		)
	}

	operationID := protocol.NewOperationID()
	if err := stream.Send(&agentv1.ModelsListEvent{
		CorrelationId: request.GetCorrelationId(),
		Value: &agentv1.ModelsListEvent_Accepted{
			Accepted: &agentv1.ModelsListAccepted{
				OperationId: string(operationID),
			},
		},
	}); err != nil {
		return err
	}

	coordinator, ok := s.resourceCoordinator.(ModelsCoordinator)
	if !ok {
		return sendModelsFailure(
			stream,
			request.GetCorrelationId(),
			operationID,
			"models listing is unavailable",
		)
	}
	models, err := coordinator.ListModels(stream.Context(), leaseID)
	if err != nil {
		return sendModelsFailure(
			stream,
			request.GetCorrelationId(),
			operationID,
			"models listing failed",
		)
	}

	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)

	var sequence uint64
	for _, name := range names {
		if err := protocol.ValidateModelID(name); err != nil {
			return sendModelsFailureAt(
				stream,
				request.GetCorrelationId(),
				operationID,
				sequence+1,
				"models listing failed",
			)
		}
		encoded, err := json.Marshal(models[name])
		if err != nil ||
			len(encoded) > protocol.MaxConfigurationBytes {
			clear(encoded)
			return sendModelsFailureAt(
				stream,
				request.GetCorrelationId(),
				operationID,
				sequence+1,
				"models listing failed",
			)
		}
		sequence++
		sendErr := stream.Send(&agentv1.ModelsListEvent{
			CorrelationId: request.GetCorrelationId(),
			Value: &agentv1.ModelsListEvent_Item{
				Item: &agentv1.ModelsListItem{
					OperationId: string(operationID),
					Sequence:    sequence,
					ModelId:     name,
					Model:       encoded,
				},
			},
		})
		clear(encoded)
		if sendErr != nil {
			return sendErr
		}
	}

	return stream.Send(&agentv1.ModelsListEvent{
		CorrelationId: request.GetCorrelationId(),
		Value: &agentv1.ModelsListEvent_Complete{
			Complete: &agentv1.ModelsListComplete{
				OperationId: string(operationID),
				Sequence:    sequence + 1,
			},
		},
	})
}

func sendModelsFailure(
	stream agentv1.AgentService_ListModelsServer,
	correlationID string,
	operationID protocol.OperationID,
	message string,
) error {
	return sendModelsFailureAt(
		stream,
		correlationID,
		operationID,
		1,
		message,
	)
}

func sendModelsFailureAt(
	stream agentv1.AgentService_ListModelsServer,
	correlationID string,
	operationID protocol.OperationID,
	sequence uint64,
	message string,
) error {
	return stream.Send(&agentv1.ModelsListEvent{
		CorrelationId: correlationID,
		Value: &agentv1.ModelsListEvent_Failed{
			Failed: &agentv1.ModelsListFailed{
				OperationId: string(operationID),
				Sequence:    sequence,
				Message:     message,
			},
		},
	})
}

// FlushModelsCache invalidates one active models resource cache.
func (s *Service) FlushModelsCache(
	_ context.Context,
	request *agentv1.ModelsCacheFlushRequest,
) (*agentv1.ModelsCacheFlushResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "models cache request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	leaseID := protocol.LeaseID(request.GetLeaseId())
	if err := protocol.ValidateLeaseID(leaseID); err != nil {
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"models lease ID is invalid",
		)
	}
	if !session.ownsLeaseID(leaseID) {
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorLeaseNotFound,
			"models resource lease is unavailable",
			false,
		)
	}

	coordinator, ok := s.resourceCoordinator.(ModelsCoordinator)
	if !ok {
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorUnavailable,
			"models cache is unavailable",
			false,
		)
	}
	if err := coordinator.FlushModelsCache(leaseID); err != nil {
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorLeaseNotFound,
			"models resource lease is unavailable",
			false,
		)
	}

	return &agentv1.ModelsCacheFlushResponse{
		CorrelationId: request.GetCorrelationId(),
	}, nil
}

// PrepareOCI starts or joins one agent-owned OCI preparation operation.
func (s *Service) PrepareOCI(
	request *agentv1.OCIPrepareRequest,
	stream agentv1.AgentService_PrepareOCIServer,
) error {
	if request == nil {
		return invalidRequest("", "OCI prepare request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return err
	}
	defer session.finish(correlationID)

	resourceID, leaseID, err := requireLease(
		session,
		request.GetResourceId(),
		request.GetLeaseId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return err
	}

	s.beginStream()
	defer s.finishStream()

	resourceStream, err := s.resourceCoordinator.OpenResource(
		stream.Context(),
		protocol.ResourceOCI,
		resourceID,
		leaseID,
	)
	if err != nil {
		return agentError(
			request.GetCorrelationId(),
			protocol.ErrorUnavailable,
			"OCI resource is unavailable",
			true,
		)
	}
	defer func() {
		s.options.Logger.DebugError(
			"close OCI preparation stream",
			resourceStream.Close(),
			"resource_id", resourceID,
		)
	}()

	follower, ok := resourceStream.(OCIResourceStream)
	if !ok {
		return agentError(
			request.GetCorrelationId(),
			protocol.ErrorInternal,
			"OCI resource does not support preparation",
			false,
		)
	}

	terminal := false
	err = follower.Follow(
		stream.Context(),
		func(event protocol.OCIEvent) error {
			if event.Kind == protocol.OCIEventComplete ||
				event.Kind == protocol.OCIEventFailed {
				terminal = true
			}

			message, err := ociEvent(
				request.GetCorrelationId(),
				event,
			)
			if err != nil {
				return err
			}

			return stream.Send(message)
		},
	)
	if err != nil && !terminal {
		return agentError(
			request.GetCorrelationId(),
			protocol.ErrorUnavailable,
			"OCI preparation stream failed",
			true,
		)
	}

	return nil
}

func ociEvent(
	correlationID string,
	event protocol.OCIEvent,
) (*agentv1.OCIPrepareEvent, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}

	message := &agentv1.OCIPrepareEvent{
		CorrelationId: correlationID,
	}

	switch event.Kind {
	case protocol.OCIEventAccepted:
		message.Value = &agentv1.OCIPrepareEvent_Accepted{
			Accepted: &agentv1.OCIPrepareAccepted{
				OperationId: string(event.OperationID),
			},
		}
	case protocol.OCIEventSnapshot:
		message.Value = &agentv1.OCIPrepareEvent_Snapshot{
			Snapshot: &agentv1.OCIPrepareSnapshot{
				OperationId: string(event.OperationID),
				Sequence:    event.Sequence,
				Progress: protocol.OCIProgressToAgent(
					*event.Progress,
				),
			},
		}
	case protocol.OCIEventProgress:
		message.Value = &agentv1.OCIPrepareEvent_Progress{
			Progress: &agentv1.OCIPrepareProgress{
				OperationId: string(event.OperationID),
				Sequence:    event.Sequence,
				Progress: protocol.OCIProgressToAgent(
					*event.Progress,
				),
			},
		}
	case protocol.OCIEventOutput:
		message.Value = &agentv1.OCIPrepareEvent_Output{
			Output: &agentv1.OCIPrepareOutput{
				OperationId: string(event.OperationID),
				Sequence:    event.Sequence,
				Source:      protocol.OCISourceToAgent(event.Source),
				Stream:      protocol.OutputStreamToAgent(event.Stream),
				Data:        append([]byte(nil), event.Data...),
			},
		}
	case protocol.OCIEventComplete:
		message.Value = &agentv1.OCIPrepareEvent_Complete{
			Complete: &agentv1.OCIPrepareComplete{
				OperationId: string(event.OperationID),
				Sequence:    event.Sequence,
				Cached:      event.Cached,
			},
		}
	case protocol.OCIEventFailed:
		message.Value = &agentv1.OCIPrepareEvent_Failed{
			Failed: &agentv1.OCIPrepareFailed{
				OperationId: string(event.OperationID),
				Sequence:    event.Sequence,
				Message:     event.Message,
			},
		}
	default:
		return nil, errors.New("unknown OCI preparation event")
	}

	return message, nil
}
