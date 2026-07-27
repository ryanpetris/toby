// Package uuid creates dependency-free UUID values for process-local protocol
// identities and deterministic application-defined identities.
package uuid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const encodedLength = 36

// NewV4 returns a random RFC 9562 UUID version 4 string.
func NewV4() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}

	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80

	return encode(value), nil
}

// NewV8 returns an RFC 9562 UUID version 8 containing 122 caller-supplied
// custom bits.
func NewV8(value [16]byte) string {
	value[6] = value[6]&0x0f | 0x80
	value[8] = value[8]&0x3f | 0x80

	return encode(value)
}

func encode(value [16]byte) string {
	var encoded [encodedLength]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])

	return string(encoded[:])
}
