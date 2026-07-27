package protocol

// Defines transport-independent active resource information.

// ResourceListItem carries one active resource without its
// configuration.
type ResourceListItem struct {
	CorrelationID CorrelationID
	Sequence      uint64
	ResourceID    ResourceID
	Kind          ResourceKind
	ActiveLeases  uint64
}
