package status

// Verifies inline clearing, generic startup failures, and exact debug, plain,
// and quiet stream policies without giving the renderer access to stdin.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
)

type lockedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func TestServicePlainModePreservesStatusAndStreamBytes(t *testing.T) {
	var presentation lockedBuffer
	var child bytes.Buffer
	service := newService(
		&presentation,
		false,
		false,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Preparing")
	stream := operation.Writer(&child)
	input := []byte("raw \x1b[31moutput\x1b[0m")
	if _, err := stream.Write(input); err != nil {
		t.Fatal(err)
	}
	operation.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}

	if got := presentation.String(); got != "Preparing...\n" {
		t.Fatalf("presentation = %q", got)
	}
	if got := child.Bytes(); !bytes.Equal(got, input) {
		t.Fatalf("child output = %q, want %q", got, input)
	}
}

func TestServicePlainModePrintsInheritedOperationScope(t *testing.T) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		false,
		false,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	parent := service.StartScopedOperation("OpenCode", "Installing")
	child := parent.StartChild("Installing")
	if child == nil {
		t.Fatal("child operation is nil")
	}
	child.Finish(nil)
	parent.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}

	want := "OpenCode: Installing...\n" +
		"OpenCode: Installing...\n"
	if got := presentation.String(); got != want {
		t.Fatalf("presentation = %q, want %q", got, want)
	}
}

func TestOperationSetLabelReportsFinalizingInPlainMode(t *testing.T) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		false,
		false,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Installing opencode")
	operation.SetLabel("Finalizing")
	operation.SetLabel("Finalizing")
	operation.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}

	want := "Installing opencode...\nFinalizing...\n"
	if got := presentation.String(); got != want {
		t.Fatalf("presentation = %q, want %q", got, want)
	}
}

func TestOperationSetLabelUpdatesInteractiveOperation(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Installing opencode")
	operation.SetLabel("Finalizing")

	service.mu.Lock()
	active := service.operations[service.active]
	service.mu.Unlock()
	if active == nil || active.label != "Finalizing" {
		t.Fatalf("active operation = %#v", active)
	}

	operation.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceDebugModeIsAppendOnlyAndBytePreserving(t *testing.T) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{Debug: true}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Checking")
	input := []byte("child\x00\x1b[31m\n")
	if _, err := operation.Writer(&presentation).Write(input); err != nil {
		t.Fatal(err)
	}
	if !service.RevealsHiddenOutput() {
		t.Fatal("debug mode does not reveal normally hidden output")
	}
	failure := errors.New("failed")
	operation.Finish(failure)
	if err := service.Finish(failure); !errors.Is(err, failure) {
		t.Fatalf("Finish() error = %v, want original failure", err)
	}

	want := append([]byte("Checking...\n"), input...)
	if got := []byte(presentation.String()); !bytes.Equal(got, want) {
		t.Fatalf("debug output = %q, want %q", got, want)
	}
	if strings.Contains(
		presentation.String(),
		ansi.SetModeAltScreenSaveCursor,
	) || strings.Contains(
		presentation.String(),
		ansi.ResetModeAltScreenSaveCursor,
	) {
		t.Fatal("debug output contains alternate-screen controls")
	}
}

func TestServiceDebugTTYUsesInlineRendererOnlyForProgress(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{Debug: true}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Pulling OCI image example")
	service.mu.Lock()
	programBeforeProgress := service.program
	service.mu.Unlock()
	if programBeforeProgress != nil {
		t.Fatal("debug renderer started before progress was available")
	}

	operation.SetProgress(Progress{
		CompletedBytes: 50,
		TotalBytes:     100,
	})
	service.mu.Lock()
	programDuringProgress := service.program
	service.mu.Unlock()
	if programDuringProgress == nil {
		t.Fatal("debug renderer did not start for progress")
	}

	operation.Finish(nil)
	service.mu.Lock()
	programAfterProgress := service.program
	service.mu.Unlock()
	if programAfterProgress != nil {
		t.Fatal("debug renderer remained active after progress completed")
	}
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceQuietModeSuppressesPresentationAndStartupStreams(
	t *testing.T,
) {
	var presentation lockedBuffer
	var child bytes.Buffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{Quiet: true}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Preparing")
	if _, err := operation.Writer(&child).Write([]byte("hidden")); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("fatal")
	operation.Finish(failure)
	if err := service.Finish(failure); !errors.Is(err, failure) {
		t.Fatalf("Finish() error = %v, want original failure", err)
	}

	if got := presentation.String(); got != "" {
		t.Fatalf("quiet presentation = %q", got)
	}
	if got := child.String(); got != "" {
		t.Fatalf("quiet startup stream = %q", got)
	}
}

func TestServiceQuietModeSuppressesDiagnostics(t *testing.T) {
	var diagnosticOutput bytes.Buffer
	diagnostics, err := diagnostic.NewService(diagnostic.Options{
		Level:  slog.LevelDebug,
		Format: diagnostic.FormatText,
		Stderr: &diagnosticOutput,
	})
	if err != nil {
		t.Fatal(err)
	}

	service := newService(
		io.Discard,
		false,
		false,
		defaultTranscriptLimit,
	)
	service.diagnostics = diagnostics
	if err := service.Begin(Options{Quiet: true}); err != nil {
		t.Fatal(err)
	}

	diagnostics.Logger("test").Warn("hidden warning")
	if got := diagnosticOutput.String(); got != "" {
		t.Fatalf("quiet diagnostic output = %q", got)
	}
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceInteractiveSuccessClearsWithoutReplay(t *testing.T) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}
	operation := service.StartOperation("Preparing")
	if _, err := operation.Writer(&presentation).
		Write([]byte("startup output\n")); err != nil {
		t.Fatal(err)
	}
	operation.Finish(nil)

	if err := service.Handoff(); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}

	output := presentation.String()
	assertInlinePresentationCleared(t, output)
}

func TestServiceInteractiveStartupFailureReturnsOnlyGenericDiagnostic(
	t *testing.T,
) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}
	operation := service.StartOperation("Preparing")
	stream := operation.Writer(&presentation)
	if _, err := stream.Write(
		[]byte("first \x1b[31mline\x1b[0m\rpartial\x00"),
	); err != nil {
		t.Fatal(err)
	}

	failure := exitcode.New(7, "sensitive startup detail")
	operation.Finish(failure)
	result := service.Finish(failure)
	if result == nil {
		t.Fatal("Finish() unexpectedly concealed the failure entirely")
	}
	if got := result.Error(); got != startupFailureMessage {
		t.Fatalf("generic startup failure = %q", got)
	}
	if got := exitcode.FromError(result); got != 7 {
		t.Fatalf("generic startup failure exit code = %d, want 7", got)
	}
	if errors.Is(result, failure) ||
		strings.Contains(result.Error(), "sensitive") {
		t.Fatalf("generic startup failure exposed detail: %v", result)
	}

	output := presentation.String()
	assertInlinePresentationCleared(t, output)
}

func TestServiceInteractiveFailureIsGenericAfterRendererAlreadyStopped(
	t *testing.T,
) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	service.started = true
	service.mode = ModeInteractive
	service.active = "test"
	service.operations["test"] = &operationState{
		id:         "test",
		label:      "Preparing",
		order:      1,
		running:    true,
		transcript: newBoundedTranscript(defaultTranscriptLimit),
	}

	err := service.Finish(errors.New("failed"))
	if err == nil || err.Error() != startupFailureMessage {
		t.Fatalf("Finish() error = %v", err)
	}
	if got := presentation.String(); got != "" {
		t.Fatalf("failure presentation = %q", got)
	}
}

func TestServicePostHandoffFailureRetainsForegroundError(t *testing.T) {
	var presentation lockedBuffer
	service := newService(
		&presentation,
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}
	if err := service.Handoff(); err != nil {
		t.Fatal(err)
	}
	service.StartOperation("Stopping")
	service.mu.Lock()
	program := service.program
	service.mu.Unlock()
	if program != nil {
		t.Fatal("post-handoff status restarted the interactive renderer")
	}

	failure := errors.New("foreground failed")
	if err := service.Finish(failure); !errors.Is(err, failure) {
		t.Fatalf("Finish() error = %v, want foreground failure", err)
	}
}

func TestServiceInteractiveOperationReplacesPriorOutput(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	first := service.StartOperation("First command")
	if _, err := first.Writer(io.Discard).Write([]byte("first output\n")); err != nil {
		t.Fatal(err)
	}
	second := service.StartOperation("Second command")
	if _, err := second.Writer(io.Discard).Write([]byte("second output\n")); err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	active := service.operations[service.active]
	service.mu.Unlock()
	if active == nil || active.id != second.id {
		t.Fatalf("active operation = %#v, want second", active)
	}
	if got := active.transcript.String(); got != "second output\n" {
		t.Fatalf("active output = %q", got)
	}
	if strings.Contains(active.transcript.String(), "Second command") {
		t.Fatalf("status label leaked into output: %q", active.transcript.String())
	}

	first.Finish(nil)
	second.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestChildOperationTemporarilyReplacesScopedParent(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	parent := service.StartScopedOperation("OpenCode", "Installing")
	if _, err := parent.Writer(io.Discard).Write(
		[]byte("parent output\n"),
	); err != nil {
		t.Fatal(err)
	}
	child := parent.StartChild("Installing")
	if child == nil {
		t.Fatal("child operation is nil")
	}
	if _, err := child.Writer(io.Discard).Write(
		[]byte("child output\n"),
	); err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	active := service.operations[service.active]
	visible := service.visibleOperationStatesLocked()
	parentState := service.operations[parent.id]
	service.mu.Unlock()

	if active == nil || active.id != child.id {
		t.Fatalf("active operation = %#v, want child", active)
	}
	if active.parent != parent.id {
		t.Fatalf("child parent = %q, want %q", active.parent, parent.id)
	}
	if active.scope != "OpenCode" {
		t.Fatalf("child scope = %q", active.scope)
	}
	if len(visible) != 1 || visible[0].id != child.id {
		t.Fatalf("visible operations = %#v, want child only", visible)
	}
	if got := active.transcript.String(); got != "child output\n" {
		t.Fatalf("child output = %q", got)
	}
	if got := parentState.transcript.String(); got != "parent output\n" {
		t.Fatalf("parent output = %q", got)
	}

	child.Finish(nil)

	service.mu.Lock()
	active = service.operations[service.active]
	visible = service.visibleOperationStatesLocked()
	service.mu.Unlock()
	if active == nil || active.id != parent.id {
		t.Fatalf("restored operation = %#v, want parent", active)
	}
	if len(visible) != 1 || visible[0].id != parent.id {
		t.Fatalf("visible operations = %#v, want parent only", visible)
	}

	parent.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestChildOperationSurvivesParentCompletion(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	parent := service.StartScopedOperation("OpenCode", "Installing")
	child := parent.StartChild("Installing")
	if child == nil {
		t.Fatal("child operation is nil")
	}
	parent.Finish(nil)

	service.mu.Lock()
	active := service.operations[service.active]
	visible := service.visibleOperationStatesLocked()
	service.mu.Unlock()
	if active == nil || active.id != child.id {
		t.Fatalf("active operation = %#v, want child", active)
	}
	if active.parent != "" {
		t.Fatalf("child parent = %q, want root", active.parent)
	}
	if len(visible) != 1 || visible[0].id != child.id {
		t.Fatalf("visible operations = %#v, want child only", visible)
	}

	child.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceInteractiveCompletionReturnsToNewestRunningOperation(
	t *testing.T,
) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	first := service.StartOperation("First command")
	if _, err := first.Writer(io.Discard).Write([]byte("first output\n")); err != nil {
		t.Fatal(err)
	}
	second := service.StartOperation("Second command")
	if _, err := second.Writer(io.Discard).Write([]byte("second output\n")); err != nil {
		t.Fatal(err)
	}
	second.Finish(nil)

	service.mu.Lock()
	active := service.operations[service.active]
	service.mu.Unlock()
	if active == nil || active.id != first.id {
		t.Fatalf("active operation = %#v, want first", active)
	}
	if got := active.transcript.String(); got != "first output\n" {
		t.Fatalf("restored output = %q", got)
	}

	first.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceLateOperationOutputCannotEnterNewPane(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	first := service.StartOperation("First command")
	firstWriter := first.Writer(io.Discard)
	first.Finish(nil)
	second := service.StartOperation("Second command")
	if _, err := firstWriter.Write([]byte("late first output\n")); err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	active := service.operations[service.active]
	service.mu.Unlock()
	if active == nil || active.id != second.id {
		t.Fatalf("active operation = %#v, want second", active)
	}
	if got := active.transcript.String(); got != "" {
		t.Fatalf("new pane contains late output: %q", got)
	}

	second.Finish(nil)
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceOperationCombinesStdoutAndStderrAndFlushesPartialOutput(
	t *testing.T,
) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Installing tool")
	stdout := operation.Writer(io.Discard)
	stderr := operation.Writer(io.Discard)
	if _, err := stdout.Write([]byte("stdout\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("partial stderr")); err != nil {
		t.Fatal(err)
	}
	operation.Finish(nil)
	operation.Finish(errors.New("duplicate completion"))

	service.mu.Lock()
	active := service.operations[service.active]
	service.mu.Unlock()
	if active == nil {
		t.Fatal("completed operation is not visible")
	}
	if got := active.transcript.String(); got !=
		"stdout\npartial stderr" {
		t.Fatalf("combined output = %q", got)
	}
	if active.failed {
		t.Fatal("repeated completion changed the terminal state")
	}

	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestOperationClearOutputDiscardsTranscriptAndPartialLines(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operation := service.StartOperation("Building OCI image")
	stdout := operation.Writer(io.Discard)
	stderr := operation.Writer(io.Discard)
	if _, err := stdout.Write([]byte("build output\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stderr.Write([]byte("partial build output")); err != nil {
		t.Fatal(err)
	}

	operation.ClearOutput()
	operation.SetLabel("Extracting OCI image")
	operation.SetProgress(Progress{
		CompletedBytes: 1,
		TotalBytes:     2,
	})
	operation.Finish(nil)

	service.mu.Lock()
	active := service.operations[service.active]
	pendingBytes := service.pendingBytes
	service.mu.Unlock()
	if active == nil {
		t.Fatal("completed operation is not visible")
	}
	if got := active.transcript.String(); got != "" {
		t.Fatalf("cleared transcript = %q", got)
	}
	if pendingBytes != 0 {
		t.Fatalf("pending output bytes = %d, want 0", pendingBytes)
	}

	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceAggregateOperationOutputRemainsBounded(t *testing.T) {
	const limit = 128
	service := newService(
		&lockedBuffer{},
		true,
		true,
		limit,
	)
	if err := service.Begin(Options{}); err != nil {
		t.Fatal(err)
	}

	operations := make([]*Operation, 0, 8)
	for index := range 8 {
		operation := service.StartOperation(
			fmt.Sprintf("Operation %d", index),
		)
		if _, err := operation.Writer(io.Discard).Write(
			bytes.Repeat([]byte{'a' + byte(index)}, 80),
		); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, operation)

		service.mu.Lock()
		retained := service.totalRetainedBytesLocked()
		bound := service.transcriptLimit
		service.mu.Unlock()
		if retained > bound {
			t.Fatalf(
				"retained output = %d bytes, limit = %d",
				retained,
				bound,
			)
		}
	}

	for _, operation := range operations {
		operation.Finish(nil)
	}
	if err := service.Finish(nil); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsConflictingModes(t *testing.T) {
	service := newService(
		&lockedBuffer{},
		true,
		true,
		defaultTranscriptLimit,
	)
	if err := service.Begin(Options{
		Debug: true,
		Quiet: true,
	}); err == nil {
		t.Fatal("conflicting debug and quiet modes were accepted")
	}
}

func TestBoundedTranscriptRetainsTailAtLimit(t *testing.T) {
	transcript := newBoundedTranscript(64)
	transcript.Append(bytes.Repeat([]byte("a"), 128))

	got := transcript.String()
	if len(got) > 64 {
		t.Fatalf("bounded transcript has %d bytes", len(got))
	}
	if !strings.HasPrefix(got, omittedMarker) {
		t.Fatalf("bounded transcript = %q", got)
	}
}

func assertInlinePresentationCleared(t *testing.T, output string) {
	t.Helper()

	if strings.Contains(output, ansi.SetModeAltScreenSaveCursor) ||
		strings.Contains(output, ansi.ResetModeAltScreenSaveCursor) {
		t.Fatalf("interactive output contains alternate-screen controls: %q", output)
	}

	terminal := vt.NewEmulator(80, 24)
	screenOutput := strings.NewReplacer(
		ansi.RequestModeSynchronizedOutput, "",
		ansi.RequestModeUnicodeCore, "",
	).Replace(output)
	if _, err := terminal.Write([]byte(screenOutput)); err != nil {
		t.Fatal(err)
	}
	if terminal.IsAltScreen() {
		t.Fatal("interactive presentation left the alternate screen active")
	}
	screen := terminal.String()
	if strings.Contains(screen, "Toby") ||
		strings.Contains(screen, "Preparing") ||
		strings.Contains(screen, "startup output") ||
		strings.Contains(screen, "first line") ||
		strings.Contains(screen, "partial") {
		t.Fatalf("interactive presentation was not cleared: %q", screen)
	}
}
