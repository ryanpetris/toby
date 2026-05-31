package sandboxbinary

import "os"

func SourceBytes() ([]byte, error) {
	return os.ReadFile("/proc/self/exe")
}
