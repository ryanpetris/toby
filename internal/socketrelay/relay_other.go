//go:build !linux

package socketrelay

// Supplies the inert relay shape used by the cross-platform Set contract.

type relay struct{}

func (r *relay) Close() error {
	return nil
}
