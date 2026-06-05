// Package sandboxbinary provides the Toby binary bytes to inject into a sandbox
// container. SourceBytes always returns a Linux binary; how it is obtained is
// platform-specific (see the build-tagged files): on Linux it is the running
// executable, and on Darwin it is read from $TOBY_LINUX_TOBY or, when built with
// the toby_embed_linux tag, from a copy embedded at build time.
package sandboxbinary
