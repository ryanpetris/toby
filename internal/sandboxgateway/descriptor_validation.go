package sandboxgateway

// Validates sandbox socket capability descriptors before mounting.

import (
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/sandbox/layout"
)

func (c DescriptorConfig) validate() error {
	if c.HostSocket == "" ||
		!filepath.IsAbs(c.HostSocket) ||
		filepath.Clean(c.HostSocket) != c.HostSocket ||
		strings.ContainsRune(c.HostSocket, 0) {
		return &DescriptorError{
			Message: "host socket path is invalid",
		}
	}
	if c.HostSocketInode == 0 {
		return &DescriptorError{
			Message: "host socket generation is invalid",
		}
	}
	if c.SandboxSocket != layout.SandboxSocket() {
		return &DescriptorError{
			Message: "sandbox socket path is not canonical",
		}
	}

	return nil
}
