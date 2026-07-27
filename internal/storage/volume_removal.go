package storage

// Defines progress notifications for persistent-volume removal.

// VolumeRemovalPhase identifies one stage of deleting a volume.
type VolumeRemovalPhase uint8

const (
	// VolumeRemovalPhaseRemoving indicates that deletion is about to begin.
	VolumeRemovalPhaseRemoving VolumeRemovalPhase = iota + 1
	// VolumeRemovalPhaseRemoved indicates that deletion completed.
	VolumeRemovalPhaseRemoved
	// VolumeRemovalPhaseFailed indicates that deletion failed.
	VolumeRemovalPhaseFailed
)

// VolumeRemovalProgress reports a state change for one selected volume.
type VolumeRemovalProgress struct {
	ID    string
	Phase VolumeRemovalPhase
}

// VolumeRemovalReporter receives ordered volume-removal progress. It cannot
// return an error to interrupt deletion.
type VolumeRemovalReporter func(VolumeRemovalProgress)

func (r VolumeRemovalReporter) report(progress VolumeRemovalProgress) {
	if r != nil {
		r(progress)
	}
}
