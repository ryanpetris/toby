package status

// Normalizes captured interactive startup streams while preserving partial
// lines until completion or renderer shutdown.

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const maximumPendingLine = 32 << 10

type streamWriter struct {
	mu sync.Mutex

	service   *Service
	operation OperationID
	pending   []byte
	closed    bool
}

var _ io.Writer = (*streamWriter)(nil)

func (w *streamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(data), nil
	}

	for remaining := data; len(remaining) != 0; {
		size := min(len(remaining), maximumPendingLine)
		w.pending = append(w.pending, remaining[:size]...)
		overLimit := w.service.changePendingBytes(size)
		w.flushCompleteLines()
		if len(w.pending) >= maximumPendingLine || overLimit {
			w.flushPending(true)
		}
		remaining = remaining[size:]
	}
	if len(w.pending) != 0 &&
		!w.service.operationActive(w.operation) {
		w.flushPending(false)
	}

	return len(data), nil
}

func (w *streamWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	w.flushPending(false)
}

func (w *streamWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	w.flushPending(false)
	w.closed = true
}

func (w *streamWriter) flushCompleteLines() {
	for {
		index := bytes.IndexAny(w.pending, "\r\n")
		if index < 0 {
			return
		}

		end := index + 1
		if w.pending[index] == '\r' &&
			end < len(w.pending) &&
			w.pending[end] == '\n' {
			end++
		}
		line := append([]byte(nil), w.pending[:index]...)
		w.pending = w.pending[end:]
		w.service.changePendingBytes(-end)
		w.service.appendOutput(
			w.operation,
			normalizeLine(line, true),
		)
	}
}

func (w *streamWriter) flushPending(split bool) {
	if len(w.pending) == 0 {
		return
	}

	data := append([]byte(nil), w.pending...)
	w.pending = w.pending[:0]
	w.service.changePendingBytes(-len(data))
	w.service.appendOutput(
		w.operation,
		normalizeLine(data, split),
	)
}

func normalizeLine(data []byte, newline bool) []byte {
	text := ansi.Strip(string(bytes.ToValidUTF8(data, []byte("\uFFFD"))))
	text = strings.Map(func(value rune) rune {
		if value == '\t' || !unicode.IsControl(value) {
			return value
		}
		return -1
	}, text)
	if newline {
		text += "\n"
	}
	return []byte(text)
}
