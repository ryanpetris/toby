package sandboxbinary

// Linux: the running executable is itself a Linux binary, so SourceBytes reads
// /proc/self/exe.

import "os"

func SourceBytes() ([]byte, error) {
	return os.ReadFile("/proc/self/exe")
}
