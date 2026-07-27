package diagnostic

// Suppresses structured handler work after foreground handoff.

import (
	"context"
	"log/slog"
)

type gateHandler struct {
	sink *sink
	next slog.Handler
}

var _ slog.Handler = (*gateHandler)(nil)

func (h *gateHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h != nil &&
		h.sink != nil &&
		!h.sink.suppressed.Load() &&
		h.next.Enabled(ctx, level)
}

func (h *gateHandler) Handle(ctx context.Context, record slog.Record) error {
	if h == nil || h.sink == nil || h.sink.suppressed.Load() {
		return nil
	}
	DiscardError(
		"the diagnostic handler cannot report its own failure",
		"handle diagnostic record",
		h.next.Handle(ctx, record),
		"level", record.Level,
		"message", record.Message,
	)
	return nil
}

func (h *gateHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &gateHandler{
		sink: h.sink,
		next: h.next.WithAttrs(attrs),
	}
}

func (h *gateHandler) WithGroup(name string) slog.Handler {
	return &gateHandler{
		sink: h.sink,
		next: h.next.WithGroup(name),
	}
}
