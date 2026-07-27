package status

// Bounded startup transcript storage used for the interactive display.

import (
	"bytes"
	"unicode/utf8"
)

const omittedMarker = "[earlier startup output omitted]\n"

type boundedTranscript struct {
	limit     int
	data      []byte
	truncated bool
}

func newBoundedTranscript(limit int) boundedTranscript {
	if limit < len(omittedMarker)+utf8.UTFMax {
		limit = len(omittedMarker) + utf8.UTFMax
	}
	return boundedTranscript{limit: limit}
}

func (b *boundedTranscript) Append(data []byte) {
	if len(data) == 0 {
		return
	}

	b.data = append(b.data, data...)
	if len(b.data) <= b.limit {
		return
	}

	keep := b.limit - len(omittedMarker)
	start := len(b.data) - keep
	for start < len(b.data) && !utf8.RuneStart(b.data[start]) {
		start++
	}
	b.data = append([]byte(nil), b.data[start:]...)
	b.truncated = true
}

func (b *boundedTranscript) Reset() {
	b.data = b.data[:0]
	b.truncated = false
}

func (b *boundedTranscript) Omit() {
	b.data = b.data[:0]
	b.truncated = true
}

func (b *boundedTranscript) Len() int {
	return len(b.data)
}

func (b *boundedTranscript) String() string {
	data := bytes.ToValidUTF8(b.data, []byte("\uFFFD"))
	if b.truncated {
		return omittedMarker + string(data)
	}
	return string(data)
}
