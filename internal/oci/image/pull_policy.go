// Package image defines the configured OCI image pull policy.
package image

// PullPolicy determines whether image preparation may use a cached object,
// contact its registry, or both.
type PullPolicy string

const (
	// PullIfMissing reuses a locally tagged image when present.
	PullIfMissing PullPolicy = "if-missing"
	// PullAlways refreshes the image reference before use.
	PullAlways PullPolicy = "always"
	// PullNever requires an existing local image.
	PullNever PullPolicy = "never"
)
