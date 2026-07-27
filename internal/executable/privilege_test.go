package executable

// Verifies host-root rejection and the Bubblewrap namespace-root exception.

import (
	"errors"
	"testing"
)

func TestCheckUnprivileged(t *testing.T) {
	if err := checkUnprivileged(1000); err != nil {
		t.Fatalf("ordinary user was rejected: %v", err)
	}
	if err := checkUnprivileged(0); !errors.Is(err, errRootExecution) {
		t.Fatalf("root error = %v, want %v", err, errRootExecution)
	}
}

func TestCheckSandboxUnprivileged(t *testing.T) {
	readError := errors.New("read UID map")
	tests := []struct {
		name         string
		effectiveUID int
		uidMap       string
		readErr      error
		wantErr      bool
		wantRead     bool
	}{
		{
			name:         "ordinary user",
			effectiveUID: 1000,
		},
		{
			name:         "Bubblewrap namespace root",
			effectiveUID: 0,
			uidMap:       "         0       1000          1\n",
			wantRead:     true,
		},
		{
			name:         "host root",
			effectiveUID: 0,
			uidMap:       "         0          0 4294967295\n",
			wantErr:      true,
			wantRead:     true,
		},
		{
			name:         "root is not mapped",
			effectiveUID: 0,
			uidMap:       "      1000       1000          1\n",
			wantErr:      true,
			wantRead:     true,
		},
		{
			name:         "malformed map",
			effectiveUID: 0,
			uidMap:       "invalid\n",
			wantErr:      true,
			wantRead:     true,
		},
		{
			name:         "unreadable map",
			effectiveUID: 0,
			readErr:      readError,
			wantErr:      true,
			wantRead:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var read bool
			err := checkSandboxUnprivileged(
				test.effectiveUID,
				func(path string) ([]byte, error) {
					read = true
					if path != procSelfUIDMap {
						t.Fatalf(
							"UID map path = %q, want %q",
							path,
							procSelfUIDMap,
						)
					}
					return []byte(test.uidMap), test.readErr
				},
			)
			if test.wantErr && !errors.Is(err, errRootExecution) {
				t.Fatalf(
					"checkSandboxUnprivileged() error = %v, want %v",
					err,
					errRootExecution,
				)
			}
			if !test.wantErr && err != nil {
				t.Fatalf(
					"checkSandboxUnprivileged() error = %v",
					err,
				)
			}
			if read != test.wantRead {
				t.Fatalf(
					"UID map read = %t, want %t",
					read,
					test.wantRead,
				)
			}
		})
	}
}

func TestMappedParentUID(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		uid       uint64
		wantUID   uint64
		wantFound bool
	}{
		{
			name:      "offset mapping",
			data:      "10 1000 5\n",
			uid:       12,
			wantUID:   1002,
			wantFound: true,
		},
		{
			name:      "later mapping",
			data:      "0 1000 1\n10 2000 5\n",
			uid:       12,
			wantUID:   2002,
			wantFound: true,
		},
		{
			name: "unmapped",
			data: "10 1000 5\n",
			uid:  20,
		},
		{
			name: "zero length",
			data: "0 1000 0\n",
			uid:  0,
		},
		{
			name: "overflow",
			data: "0 18446744073709551615 2\n",
			uid:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := mappedParentUID(
				[]byte(test.data),
				test.uid,
			)
			if got != test.wantUID || found != test.wantFound {
				t.Fatalf(
					"mappedParentUID() = (%d, %t), want (%d, %t)",
					got,
					found,
					test.wantUID,
					test.wantFound,
				)
			}
		})
	}
}
