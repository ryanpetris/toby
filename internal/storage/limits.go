package storage

// Defines hard upper bounds for first-use volume seeding.

// Limits bounds storage metadata and seed traversal.
type Limits struct {
	SeedEntries  uint64
	SeedBytes    uint64
	MetadataSize int64
	PathBytes    int
	Depth        int
}

// DefaultLimits returns the production storage bounds.
func DefaultLimits() Limits {
	return Limits{
		SeedEntries:  1_000_000,
		SeedBytes:    16 << 30,
		MetadataSize: 64 << 10,
		PathBytes:    4096,
		Depth:        100,
	}
}

func (l Limits) validate() error {
	maximum := DefaultLimits()
	if l.SeedEntries == 0 || l.SeedBytes == 0 || l.MetadataSize <= 0 ||
		l.PathBytes <= 0 || l.Depth <= 0 ||
		l.SeedEntries > maximum.SeedEntries ||
		l.SeedBytes > maximum.SeedBytes ||
		l.MetadataSize > maximum.MetadataSize ||
		l.PathBytes > maximum.PathBytes ||
		l.Depth > maximum.Depth {
		return errInvalidLimits
	}

	return nil
}
