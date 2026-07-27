// Package image defines the configured OCI image pull policy.
package image

// PullPolicy determines whether image preparation may use a cached object,
// rematerialize its configured source, or both.
type PullPolicy string

const (
	// PullIfMissing reuses a locally tagged image when present.
	PullIfMissing PullPolicy = "if-missing"
	// PullAlways rematerializes the configured source before use.
	PullAlways PullPolicy = "always"
	// PullNever requires an existing local image.
	PullNever PullPolicy = "never"
)
