//go:build darwin && !toby_embed_linux

package sandboxbinary

// Darwin (default build): no embedded binary, so SourceBytes reads the Linux
// binary from the path in $TOBY_LINUX_TOBY.

import (
	"fmt"
	"os"
)

const EnvLinuxToby = "TOBY_LINUX_TOBY"

func SourceBytes() ([]byte, error) {
	path := os.Getenv(EnvLinuxToby)
	if path == "" {
		return nil, fmt.Errorf("%s must point to a Linux Toby binary for this Darwin build", EnvLinuxToby)
	}
	return os.ReadFile(path)
}
