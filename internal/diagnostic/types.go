package diagnostic

// Diagnostic configuration types.

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

const (
	logFormatEnvironment = "TOBY_LOG_FORMAT"
	logLevelEnvironment  = "TOBY_LOG_LEVEL"
)

// Format selects the diagnostic record encoding.
type Format uint8

const (
	// FormatText emits compact source-prefixed messages.
	FormatText Format = iota
	// FormatJSON emits one structured JSON object per record.
	FormatJSON
)

// Options configures one process-wide diagnostic service.
type Options struct {
	Level  slog.Level
	Format Format
	Stderr io.Writer
}

// OptionsFromEnvironment builds diagnostic options from the process
// environment.
func OptionsFromEnvironment() (Options, error) {
	options := Options{
		Level:  slog.LevelInfo,
		Format: FormatText,
		Stderr: os.Stderr,
	}

	switch value := strings.ToLower(
		strings.TrimSpace(os.Getenv(logFormatEnvironment)),
	); value {
	case "", "text":
	case "json":
		options.Format = FormatJSON
	default:
		return Options{}, fmt.Errorf(
			"%s must be text or json",
			logFormatEnvironment,
		)
	}

	if value := strings.TrimSpace(os.Getenv(logLevelEnvironment)); value != "" {
		if err := options.Level.UnmarshalText([]byte(value)); err != nil {
			return Options{}, fmt.Errorf(
				"%s: %w",
				logLevelEnvironment,
				err,
			)
		}
	}

	return options, nil
}
