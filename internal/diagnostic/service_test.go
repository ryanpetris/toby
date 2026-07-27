package diagnostic

// Diagnostic formatting, filtering, adapters, and failure containment.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestTextLoggerIncludesSourceAndMessage(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatText,
		Stderr: &output,
	})

	service.Logger("oci.catalog").Debug(
		"close OCI descriptor: permission denied",
		"operation", "close_descriptor",
		"error", errors.New("permission denied"),
	)

	if got, want := output.String(),
		"oci.catalog: close OCI descriptor: permission denied\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestJSONLoggerIncludesSourceLevelAndAttributes(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatJSON,
		Stderr: &output,
	})

	service.Logger("agent.server").ErrorError(
		"start MCP resource",
		errors.New("unavailable"),
		"resource_id", "resource-1",
	)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON record: %v", err)
	}
	if got := record["source"]; got != "agent.server" {
		t.Fatalf("source = %#v, want agent.server", got)
	}
	if got := record["level"]; got != "ERROR" {
		t.Fatalf("level = %#v, want ERROR", got)
	}
	if got := record["msg"]; got != "start MCP resource: unavailable" {
		t.Fatalf("msg = %#v, want start MCP resource failure", got)
	}
	if got := record["resource_id"]; got != "resource-1" {
		t.Fatalf("resource_id = %#v, want resource-1", got)
	}
	if got := record["error"]; got != "unavailable" {
		t.Fatalf("error = %#v, want unavailable", got)
	}
}

func TestLoggerFiltersBelowConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelWarn,
		Format: FormatText,
		Stderr: &output,
	})
	logger := service.Logger("test")

	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warning")

	if got, want := output.String(), "test: warning\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestBeginForegroundSuppressesAllOutput(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatText,
		Stderr: &output,
	})
	logger := service.Logger("test")
	writer := service.Stderr()

	logger.Info("before")
	service.BeginForeground()
	logger.Warn("after")
	if _, err := writer.Write([]byte("raw after\n")); err != nil {
		t.Fatalf("write suppressed stderr: %v", err)
	}

	if got, want := output.String(), "test: before\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestBeginQuietSuppressesAllOutput(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatText,
		Stderr: &output,
	})
	logger := service.Logger("test")
	writer := service.Stderr()

	service.BeginQuiet()
	logger.Warn("hidden")
	if _, err := writer.Write([]byte("raw hidden\n")); err != nil {
		t.Fatalf("write suppressed stderr: %v", err)
	}

	if got := output.String(); got != "" {
		t.Fatalf("quiet diagnostic output = %q", got)
	}
}

func TestBeginForegroundWaitsForInFlightOutput(t *testing.T) {
	output := &blockingWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := newTestService(t, Options{
		Level:  slog.LevelInfo,
		Format: FormatText,
		Stderr: output,
	})

	written := make(chan struct{})
	go func() {
		service.Logger("test").Info("before")
		close(written)
	}()
	<-output.started

	suppressed := make(chan struct{})
	go func() {
		service.BeginForeground()
		close(suppressed)
	}()
	for !service.sink.suppressed.Load() {
		runtime.Gosched()
	}

	select {
	case <-suppressed:
		t.Fatal("BeginForeground returned while output was still in flight")
	default:
	}

	close(output.release)
	<-written
	<-suppressed

	service.Logger("test").Info("after")
	if got, want := output.String(), "test: before\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSinkFailuresAreDiscarded(t *testing.T) {
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatJSON,
		Stderr: failingWriter{},
	})

	service.Logger("test").Warn("failure", "error", errors.New("broken"))
	data := []byte("raw")
	if count, err := service.Stderr().Write(data); err != nil || count != len(data) {
		t.Fatalf("Write() = %d, %v; want %d, nil", count, err, len(data))
	}
}

func TestStandardLoggerUsesBoundSource(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatText,
		Stderr: &output,
	})

	service.Logger("http.server").StandardLogger(slog.LevelWarn).Print(
		"accept failed",
	)

	if got, want := output.String(), "http.server: accept failed\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLoggerWithCarriesAdditionalAttributes(t *testing.T) {
	var output bytes.Buffer
	service := newTestService(t, Options{
		Level:  slog.LevelDebug,
		Format: FormatJSON,
		Stderr: &output,
	})

	service.Logger("gateway.models").
		With("resource_id", "resource-1").
		Info("ready")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON record: %v", err)
	}
	if got := record["resource_id"]; got != "resource-1" {
		t.Fatalf("resource_id = %#v, want resource-1", got)
	}
}

func TestNilLoggerIsNonFailing(t *testing.T) {
	var logger *Logger
	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")
	logger.DebugError("debug", nil)
	logger.WarnError("warn", nil)
	logger.ErrorError("error", nil)

	if got := logger.With("key", "value"); got == nil {
		t.Fatal("With() returned nil")
	}
	if got := logger.StandardLogger(slog.LevelInfo); got == nil {
		t.Fatal("StandardLogger() returned nil")
	}
}

func newTestService(t *testing.T, options Options) *Service {
	t.Helper()

	service, err := NewService(options)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

var _ io.Writer = failingWriter{}

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	output bytes.Buffer
}

func (w *blockingWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
	})
	<-w.release

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(data)
}

func (w *blockingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestOptionsFromEnvironment(t *testing.T) {
	t.Setenv(logFormatEnvironment, "json")
	t.Setenv(logLevelEnvironment, "debug")

	options, err := OptionsFromEnvironment()
	if err != nil {
		t.Fatalf("OptionsFromEnvironment() error = %v", err)
	}
	if options.Format != FormatJSON {
		t.Fatalf("Format = %v, want JSON", options.Format)
	}
	if options.Level != slog.LevelDebug {
		t.Fatalf("Level = %v, want debug", options.Level)
	}
}

func TestOptionsFromEnvironmentRejectsInvalidValues(t *testing.T) {
	t.Setenv(logFormatEnvironment, "yaml")

	_, err := OptionsFromEnvironment()
	if err == nil || !strings.Contains(err.Error(), logFormatEnvironment) {
		t.Fatalf("error = %v, want %s diagnostic", err, logFormatEnvironment)
	}
}
