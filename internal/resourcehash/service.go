package resourcehash

// Implements the agent's single canonical BLAKE2b-512 identity service.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/blake2b"

	"petris.dev/toby/internal/uuid"
)

// Algorithm is the canonical resource digest algorithm.
const Algorithm = "blake2b-512"

// Digest is one complete canonical resource identity hash.
type Digest struct {
	sum [blake2b.Size]byte
}

var _ fmt.Stringer = Digest{}

// String returns the algorithm-qualified lowercase digest.
func (d Digest) String() string {
	var encoded [blake2b.Size * 2]byte
	hex.Encode(encoded[:], d.sum[:])

	return Algorithm + ":" + string(encoded[:])
}

// UUID returns the deterministic UUID version 8 derived from the digest's
// first 128 bits.
func (d Digest) UUID() string {
	var custom [16]byte
	copy(custom[:], d.sum[:len(custom)])

	return uuid.NewV8(custom)
}

// IsZero reports whether the digest was never computed.
func (d Digest) IsZero() bool {
	return d == Digest{}
}

// Service computes canonical resource identities. Resource-specific services
// remain responsible for applying defaults and constructing typed identity
// documents before calling Sum.
type Service struct{}

// NewService constructs the process-wide agent hashing service.
func NewService() *Service {
	return &Service{}
}

// Sum canonically serializes value and returns its BLAKE2b-512 identity.
func (s *Service) Sum(value any) (Digest, error) {
	if s == nil {
		return Digest{}, errors.New("resource hashing service is nil")
	}

	document, err := json.Marshal(value)
	if err != nil {
		return Digest{}, fmt.Errorf(
			"serialize canonical resource identity: %w",
			err,
		)
	}
	defer clear(document)

	return Digest{sum: blake2b.Sum512(document)}, nil
}
