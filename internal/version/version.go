package version

// Version is overridden at build time with -ldflags "-X .../version.Version=<version>".
var Version = "dev"

func String() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
