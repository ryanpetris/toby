package diagnostic

// Source-bound structured logger surface and standard-library adapter.

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"

	"petris.dev/toby/internal/diagnostic/warning"
)

// Logger emits structured records for one stable logical source.
type Logger struct {
	logger *slog.Logger
}

var _ warning.Logger = (*Logger)(nil)

// Debug emits a debug record.
func (l *Logger) Debug(message string, args ...any) {
	if l != nil && l.logger != nil {
		l.logger.Debug(message, args...)
	}
}

// Info emits an informational record.
func (l *Logger) Info(message string, args ...any) {
	if l != nil && l.logger != nil {
		l.logger.Info(message, args...)
	}
}

// Warn emits a warning record.
func (l *Logger) Warn(message string, args ...any) {
	if l != nil && l.logger != nil {
		l.logger.Warn(message, args...)
	}
}

// Error emits an error record.
func (l *Logger) Error(message string, args ...any) {
	if l != nil && l.logger != nil {
		l.logger.Error(message, args...)
	}
}

// DebugError emits a debug record whose human-readable message and structured
// attributes both retain the supplied error.
func (l *Logger) DebugError(message string, err error, args ...any) {
	l.logError(slog.LevelDebug, message, err, args...)
}

// WarnError emits a warning record whose human-readable message and structured
// attributes both retain the supplied error.
func (l *Logger) WarnError(message string, err error, args ...any) {
	l.logError(slog.LevelWarn, message, err, args...)
}

// ErrorError emits an error record whose human-readable message and structured
// attributes both retain the supplied error.
func (l *Logger) ErrorError(message string, err error, args ...any) {
	l.logError(slog.LevelError, message, err, args...)
}

// With returns a logger carrying additional structured context.
func (l *Logger) With(args ...any) *Logger {
	if l == nil || l.logger == nil {
		return &Logger{}
	}
	return &Logger{logger: l.logger.With(args...)}
}

// StandardLogger adapts the logger for standard-library APIs.
func (l *Logger) StandardLogger(level slog.Level) *log.Logger {
	if l == nil || l.logger == nil {
		return log.New(io.Discard, "", 0)
	}
	return slog.NewLogLogger(l.logger.Handler(), level)
}

func (l *Logger) logError(
	level slog.Level,
	message string,
	err error,
	args ...any,
) {
	if err == nil {
		return
	}
	if l == nil || l.logger == nil {
		DiscardError(
			"no diagnostic logger is available",
			message,
			err,
			args...,
		)
		return
	}

	values := append([]any(nil), args...)
	values = append(values, "error", err)
	l.logger.Log(
		context.Background(),
		level,
		fmt.Sprintf("%s: %v", message, err),
		values...,
	)
}
