package oci

// Defines classifiable image-removal conflicts and progress notifications.

import "errors"

var (
	// ErrImageBusy reports that an image reference is being prepared or its
	// immutable object is retained by a running sandbox.
	ErrImageBusy = errors.New("OCI image is in use")
)

// ImageRemovalPhase identifies one user-visible removal state.
type ImageRemovalPhase uint8

const (
	// ImageRemovalPhaseRemoving means removal is in progress.
	ImageRemovalPhaseRemoving ImageRemovalPhase = iota + 1
	// ImageRemovalPhaseRemoved means the object was deleted.
	ImageRemovalPhaseRemoved
	// ImageRemovalPhaseUntagged means only the reference was deleted.
	ImageRemovalPhaseUntagged
	// ImageRemovalPhaseFailed means removal failed.
	ImageRemovalPhaseFailed
)

// ImageRemovalProgress reports one selected catalog entry's state.
type ImageRemovalProgress struct {
	ID    string
	Phase ImageRemovalPhase
}

// ImageRemovalReporter receives ordered image-removal progress.
type ImageRemovalReporter func(ImageRemovalProgress)

func (r ImageRemovalReporter) report(progress ImageRemovalProgress) {
	if r != nil {
		r(progress)
	}
}
