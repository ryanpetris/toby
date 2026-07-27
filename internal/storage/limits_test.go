package storage

// Tests that configurable first-use limits can only tighten the production
// hard bounds.

import (
	"errors"
	"testing"
)

func TestLimitsValidationAcceptsDefaultsAndTighterBounds(t *testing.T) {
	if err := DefaultLimits().validate(); err != nil {
		t.Fatal(err)
	}

	tighter := Limits{
		SeedEntries:  1,
		SeedBytes:    1,
		MetadataSize: 1,
		PathBytes:    1,
		Depth:        1,
	}
	if err := tighter.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLimitsValidationRejectsZeroAndRaisedBounds(t *testing.T) {
	maximum := DefaultLimits()
	tests := []Limits{
		{},
		{
			SeedEntries:  maximum.SeedEntries + 1,
			SeedBytes:    maximum.SeedBytes,
			MetadataSize: maximum.MetadataSize,
			PathBytes:    maximum.PathBytes,
			Depth:        maximum.Depth,
		},
		{
			SeedEntries:  maximum.SeedEntries,
			SeedBytes:    maximum.SeedBytes + 1,
			MetadataSize: maximum.MetadataSize,
			PathBytes:    maximum.PathBytes,
			Depth:        maximum.Depth,
		},
		{
			SeedEntries:  maximum.SeedEntries,
			SeedBytes:    maximum.SeedBytes,
			MetadataSize: maximum.MetadataSize + 1,
			PathBytes:    maximum.PathBytes,
			Depth:        maximum.Depth,
		},
		{
			SeedEntries:  maximum.SeedEntries,
			SeedBytes:    maximum.SeedBytes,
			MetadataSize: maximum.MetadataSize,
			PathBytes:    maximum.PathBytes + 1,
			Depth:        maximum.Depth,
		},
		{
			SeedEntries:  maximum.SeedEntries,
			SeedBytes:    maximum.SeedBytes,
			MetadataSize: maximum.MetadataSize,
			PathBytes:    maximum.PathBytes,
			Depth:        maximum.Depth + 1,
		},
	}

	for _, limits := range tests {
		if err := limits.validate(); !errors.Is(err, errInvalidLimits) {
			t.Errorf("validate(%#v) error = %v, want errInvalidLimits", limits, err)
		}
	}
}
