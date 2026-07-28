package sidecar

// Declares the image-preparation and background-execution boundaries used by
// the concrete sidecar provider.

import (
	"context"
	"io"
	"os"
	"reflect"

	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/pasta"
)

// Image is one leased immutable OCI rootfs plus its verified metadata.
type Image interface {
	io.Closer
	// Metadata returns the prepared image metadata.
	Metadata() oci.Metadata
	// RootfsPath returns the prepared root filesystem path.
	RootfsPath() string
	// RootfsFile opens the prepared root filesystem.
	RootfsFile() (*os.File, error)
	// Spec returns the prepared image specification.
	Spec() oci.Spec
}

// ImagePreparer resolves and leases one image reference for the native
// platform.
type ImagePreparer interface {
	// PrepareImage prepares and leases one OCI image.
	PrepareImage(
		context.Context,
		string,
		mcpgateway.ProgressReporter,
	) (Image, error)
}

// BackgroundExecutor starts fixed noninteractive Bubblewrap invocations.
type BackgroundExecutor interface {
	// StartBackground launches a noninteractive Bubblewrap process.
	StartBackground(
		context.Context,
		*bwrap.Invocation,
		bwrap.ProcessIO,
		bwrap.BackgroundSetup,
	) (bwrap.BackgroundProcess, error)
}

var _ BackgroundExecutor = (*bwrap.Executor)(nil)

// PrivateNetworkStarter connects one held sidecar network namespace.
type PrivateNetworkStarter interface {
	// Start connects the namespace and returns its owned Pasta process.
	Start(
		context.Context,
		pasta.StartOptions,
	) (pasta.Process, error)
}

var _ PrivateNetworkStarter = (*pasta.Service)(nil)

// Provider resolves image metadata, pins explicit mounts, and prepares exact
// sidecar launches. Implementations may initialize the native runtime lazily.
type Provider interface {
	// Resolve computes sidecar metadata without retaining capabilities.
	Resolve(
		context.Context,
		Definition,
		mcpgateway.ProgressReporter,
	) (Metadata, error)
	// Prepare resolves and pins a complete sidecar launch.
	Prepare(
		context.Context,
		Definition,
		mcpgateway.ProgressReporter,
	) (*Prepared, error)
	// PinMounts pins configured mount sources.
	PinMounts(
		context.Context,
		[]mcpgateway.Mount,
	) (*MountCapabilities, error)
	// PreparePinned prepares a launch using already pinned mounts.
	PreparePinned(
		context.Context,
		Definition,
		*MountCapabilities,
		mcpgateway.ProgressReporter,
	) (*Prepared, error)
}

var _ Provider = (*Preparer)(nil)

func isNilContract(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
