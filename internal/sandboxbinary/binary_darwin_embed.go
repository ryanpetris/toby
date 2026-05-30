//go:build darwin && toby_embed_linux

package sandboxbinary

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
