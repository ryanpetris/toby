package control

// Command identifiers: NewCommandID mints the UUID that correlates a command.run
// request with its later command.exit notification.

import (
	"github.com/google/uuid"
)

func NewCommandID() (string, error) {
	return uuid.NewString(), nil
}
