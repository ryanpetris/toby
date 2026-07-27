//go:build !linux

package bwrap

// Rejects exact process-identity retention on platforms without Linux pidfds.

import "fmt"

type processIdentity struct{}

func openProcessIdentity(int, int) (*processIdentity, error) {
	return nil, fmt.Errorf("exact process identities require Linux pidfds")
}

func (p *processIdentity) Exited() (bool, error) {
	return false, fmt.Errorf("exact process identities require Linux pidfds")
}

func (p *processIdentity) Kill() error {
	return nil
}

func (p *processIdentity) Close() error {
	return nil
}
