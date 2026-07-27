//go:build !linux

package resourcelog

// Rejects agent resource logs on unsupported platforms.

import (
	"fmt"
	"os"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
)

// Service is the unsupported-platform resource log service.
type Service struct{}

// NewService constructs the unsupported-platform resource log service.
func NewService(config.Paths, *diagnostic.Service) *Service {
	return &Service{}
}

// Create reports that resource logs are unavailable.
func (*Service) Create(
	protocol.ResourceKind,
	protocol.ResourceID,
	protocol.OperationID,
) (*os.File, error) {
	return nil, fmt.Errorf("resource logs are unsupported")
}

// Open reports that resource logs are unavailable.
func (*Service) Open(
	protocol.ResourceKind,
	protocol.ResourceID,
	protocol.OperationID,
) (*os.File, protocol.OperationID, error) {
	return nil, "", fmt.Errorf("resource logs are unsupported")
}
