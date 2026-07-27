// Package imagesource defines lightweight OCI source configuration shared by
// host configuration and the image runtime.
package imagesource

// Kind identifies how an OCI image layout is materialized.
type Kind string

const (
	// Registry copies an image from an OCI registry.
	Registry Kind = "registry"
	// Archive reads an OCI image-layout tar archive.
	Archive Kind = "archive"
	// Build runs Buildah and reads its OCI output.
	Build Kind = "build"
)

// BuildConfig selects one Dockerfile build context.
type BuildConfig struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}
