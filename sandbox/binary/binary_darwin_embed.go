//go:build darwin && toby_embed_linux

package sandboxbinary

// Darwin (toby_embed_linux build): SourceBytes returns the Linux binary embedded
// at build time under embedded/toby-linux.

import (
	"embed"
	"fmt"
)

//go:embed embedded/toby-linux
var embedded embed.FS

func SourceBytes() ([]byte, error) {
	data, err := embedded.ReadFile("embedded/toby-linux")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("embedded Linux Toby binary is empty")
	}
	return data, nil
}
