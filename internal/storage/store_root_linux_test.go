//go:build linux

package storage

// Verifies that persistent-data boundary descriptors are independent, exact,
// and unavailable after store closure.

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"petris.dev/toby/internal/config"
)

func TestServiceDataRootFileRetainsExactRoot(t *testing.T) {
	base := secureStorageTestPath(t)
	service := newStorageTestStore(t, base)

	file, err := service.DataRootFile()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(config.Paths{XDGDataHome: base}.TobyDataDir())
	if err != nil {
		t.Fatal(errors.Join(err, file.Close()))
	}
	got, err := file.Stat()
	if err != nil || !os.SameFile(got, want) {
		t.Fatal(errors.Join(
			fmt.Errorf("persistent-data descriptor is not exact: %v", err),
			file.Close(),
		))
	}

	if err := service.Close(); err != nil {
		t.Fatal(errors.Join(err, file.Close()))
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal(errors.Join(
			fmt.Errorf(
				"caller-owned persistent-data descriptor followed store close: %w",
				err,
			),
			file.Close(),
		))
	}
	if _, err := service.DataRootFile(); err == nil {
		t.Fatal(errors.Join(
			errors.New("closed store returned a data-root descriptor"),
			file.Close(),
		))
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
