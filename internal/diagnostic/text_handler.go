package diagnostic

// Compact human-readable slog handler.

import (
	"context"
	"log/slog"
	"strings"
)

type textHandler struct {
	sink   *sink
	level  slog.Leveler
	source string
}

var _ slog.Handler = (*textHandler)(nil)

func (h *textHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h != nil &&
		h.sink != nil &&
		!h.sink.suppressed.Load() &&
		level >= h.level.Level()
}

func (h *textHandler) Handle(_ context.Context, record slog.Record) error {
	if h == nil || h.sink == nil || h.sink.suppressed.Load() {
		return nil
	}

	message := strings.TrimRight(record.Message, "\r\n")
	if h.source != "" {
		message = h.source + ": " + message
	}
	_, err := h.sink.Write([]byte(message + "\n"))
	DiscardError(
		"the diagnostic handler cannot report its own failure",
		"write text diagnostic record",
		err,
		"level", record.Level,
	)

	return nil
}

func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	result := *h
	for _, attr := range attrs {
		if attr.Key == "source" {
			result.source = attr.Value.String()
		}
	}
	return &result
}

func (h *textHandler) WithGroup(string) slog.Handler {
	result := *h
	return &result
}
