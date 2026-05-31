package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorExitCodeAndFormatting(t *testing.T) {
	err := New(7, "failed: %s", "nope")
	if err.ExitCode() != 7 || err.Error() != "failed: nope" {
		t.Fatalf("error = %q, code = %d", err.Error(), err.ExitCode())
	}
	if empty := Code(0); empty.ExitCode() != 1 || empty.Error() != "" {
		t.Fatalf("empty = %q, code = %d", empty.Error(), empty.ExitCode())
	}
}

func TestErrorUnwrapAndFromError(t *testing.T) {
	sentinel := errors.New("sentinel")
	coded := New(9, "wrap: %w", sentinel)
	if !errors.Is(coded, sentinel) {
		t.Fatal("coded error should unwrap sentinel")
	}
	if got := FromError(nil); got != 0 {
		t.Fatalf("FromError(nil) = %d", got)
	}
	if got := FromError(fmt.Errorf("outer: %w", coded)); got != 9 {
		t.Fatalf("FromError(coded) = %d", got)
	}
	if got := FromError(errors.New("plain")); got != 1 {
		t.Fatalf("FromError(plain) = %d", got)
	}
}
