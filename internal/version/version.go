package version

import "runtime/debug"

// Version is overridden at build time with -ldflags "-X .../version.Version=<version>".
var Version = "dev"

var readBuildInfo = debug.ReadBuildInfo

func String() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if info, ok := readBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	if Version != "" {
		return Version
	}
	return "dev"
}
