package resourcehandler

// Delegates operation-log creation to the agent's shared resource-log
// service.

import (
	"os"

	"petris.dev/toby/internal/agent/protocol"
)

func (h *Service) createOperationLog(
	resourceID protocol.ResourceID,
	operationID protocol.OperationID,
) *os.File {
	file, err := h.logs.Create(
		protocol.ResourceOCI,
		resourceID,
		operationID,
	)
	if err != nil {
		h.logger.DebugError(
			"create OCI operation log",
			err,
			"resource_id",
			resourceID,
			"operation_id",
			operationID,
		)
		return nil
	}

	return file
}
