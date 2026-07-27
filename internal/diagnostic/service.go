package diagnostic

// Fx-owned diagnostic configuration, output, and logger construction.

import (
	"fmt"
	"io"
	"log/slog"
)

// Service owns diagnostic encoding, filtering, output, and process-lifetime
// suppression for one Toby process.
type Service struct {
	format  Format
	sink    *sink
	handler slog.Handler
}

// NewService constructs a process-wide diagnostic service.
func NewService(options Options) (*Service, error) {
	switch options.Format {
	case FormatText, FormatJSON:
	default:
		return nil, fmt.Errorf(
			"unsupported diagnostic format %d",
			options.Format,
		)
	}

	output := newSink(options.Stderr)
	level := new(slog.LevelVar)
	level.Set(options.Level)

	var handler slog.Handler
	if options.Format == FormatJSON {
		handler = slog.NewJSONHandler(
			output,
			&slog.HandlerOptions{Level: level},
		)
	} else {
		handler = &textHandler{
			sink:  output,
			level: level,
		}
	}
	handler = &gateHandler{
		sink: output,
		next: handler,
	}

	return &Service{
		format:  options.Format,
		sink:    output,
		handler: handler,
	}, nil
}

// Logger returns a lightweight logger bound to a stable logical source.
func (s *Service) Logger(source string) *Logger {
	if s == nil || s.handler == nil {
		return &Logger{}
	}

	return &Logger{
		logger: slog.New(s.handler).With("source", source),
	}
}

// Stderr returns a non-failing writer suppressed after foreground handoff.
func (s *Service) Stderr() io.Writer {
	if s == nil || s.sink == nil {
		return io.Discard
	}
	return s.sink
}

// Format returns the configured diagnostic encoding.
func (s *Service) Format() Format {
	if s == nil {
		return FormatText
	}
	return s.format
}

// BeginForeground permanently suppresses diagnostics for this process.
func (s *Service) BeginForeground() {
	s.suppress()
}

// BeginQuiet permanently suppresses diagnostics for this process.
func (s *Service) BeginQuiet() {
	s.suppress()
}

func (s *Service) suppress() {
	if s == nil {
		return
	}

	s.sink.suppress()
}
