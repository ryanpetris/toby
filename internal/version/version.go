// Package version reports the Toby build version. Current is overridden at build
// time via -ldflags; String resolves the effective version, falling back to the
// embedded build info. UserAgent is the product token for outbound HTTP requests.
package version

import "runtime/debug"

// Current is overridden at build time with -ldflags "-X .../version.Current=<version>".
var Current = "dev"

// UserAgent is the product token Toby sends on outbound HTTP requests.
const UserAgent = "petris-toby/1"

// String returns the current Toby build version.
func String() string {
	if Current != "" && Current != "dev" {
		return Current
	}

	info, ok := debug.ReadBuildInfo()
	return resolve(Current, info, ok)
}

func resolve(current string, info *debug.BuildInfo, ok bool) string {
	if current != "" && current != "dev" {
		return current
	}
	if ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
	}
	if current != "" {
		return current
	}
	return "dev"
}
