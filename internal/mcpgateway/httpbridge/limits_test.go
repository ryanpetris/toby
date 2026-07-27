package httpbridge

// Exercises exact-boundary, oversized, and multi-event framing limits.

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFramingLimitReadCloser(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		limit     int
		mode      framingMode
		want      string
		oversized bool
	}{
		{
			name:  "exact body",
			input: "1234",
			limit: 4,
			mode:  framingBody,
			want:  "1234",
		},
		{
			name:      "oversized body",
			input:     "12345",
			limit:     4,
			mode:      framingBody,
			want:      "1234",
			oversized: true,
		},
		{
			name:  "SSE events reset",
			input: "data:x\n\ndata:y\n\n",
			limit: 8,
			mode:  framingEvent,
			want:  "data:x\n\ndata:y\n\n",
		},
		{
			name:      "oversized SSE event",
			input:     "data:xx\n\n",
			limit:     8,
			mode:      framingEvent,
			want:      "data:xx\n",
			oversized: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var limitCalls atomic.Int64
			reader := newFramingLimitReadCloser(
				io.NopCloser(strings.NewReader(test.input)),
				test.limit,
				test.mode,
				func() {
					limitCalls.Add(1)
				},
			)

			data, err := io.ReadAll(reader)
			if string(data) != test.want {
				t.Fatalf("data = %q, want %q", data, test.want)
			}
			if test.oversized {
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatalf("error = %v, want ErrMessageTooLarge", err)
				}
				if got := limitCalls.Load(); got != 1 {
					t.Fatalf("limit callbacks = %d, want 1", got)
				}
			} else {
				if err != nil {
					t.Fatalf("ReadAll: %v", err)
				}
				if got := limitCalls.Load(); got != 0 {
					t.Fatalf("limit callbacks = %d, want 0", got)
				}
			}
		})
	}
}

func TestLineLimitReadCloser(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		limit          int
		want           string
		oversized      bool
		invalidFraming bool
	}{
		{
			name:  "exact lines reset",
			input: "1234\n5678\n",
			limit: 4,
			want:  "1234\n5678\n",
		},
		{
			name:      "oversized line",
			input:     "12345\n",
			limit:     4,
			oversized: true,
		},
		{
			name:           "multiline JSON rejected",
			input:          "{\n}\n",
			limit:          4,
			invalidFraming: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := newLineLimitReadCloser(
				io.NopCloser(strings.NewReader(test.input)),
				test.limit,
			)

			data, err := io.ReadAll(reader)
			if string(data) != test.want {
				t.Fatalf("data = %q, want %q", data, test.want)
			}
			switch {
			case test.oversized && !errors.Is(err, ErrMessageTooLarge):
				t.Fatalf("error = %v, want ErrMessageTooLarge", err)
			case test.invalidFraming && err == nil:
				t.Fatal("multiline JSON was accepted")
			case !test.oversized && !test.invalidFraming && err != nil:
				t.Fatalf("ReadAll: %v", err)
			}
		})
	}
}
