//go:build !linux

package agent

// Uses the normal agent socket outside Linux.

func preferredAgentSocket(normal string) (string, bool) {
	return normal, false
}
