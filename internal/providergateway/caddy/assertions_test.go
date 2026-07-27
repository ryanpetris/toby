package caddy

// Provides platform-independent security assertions for client tests.

import (
	"strings"
	"testing"
)

func assertRedactedError(
	t *testing.T,
	err error,
	forbidden ...string,
) {
	t.Helper()

	if err == nil {
		t.Fatal("operation unexpectedly succeeded")
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Errorf("error %q contains protected value %q", err, value)
		}
	}
}
