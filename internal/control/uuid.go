package control

import (
	"github.com/google/uuid"
)

func NewCommandID() (string, error) {
	return uuid.NewString(), nil
}
