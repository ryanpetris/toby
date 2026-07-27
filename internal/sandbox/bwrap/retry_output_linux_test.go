//go:build linux

package bwrap

// Verifies provenance-based output commitment, exact bytes, bounded buffering,
// and preservation of managed-terminal stream identity.

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestPayloadOutputGateDiscardsUncommittedOutput(t *testing.T) {
	var output bytes.Buffer
	gate := newPayloadOutputGate()
	if err := gate.attach(&output); err != nil {
		t.Fatal(err)
	}

	diagnostic := []byte("bwrap: transient overlay failure\n")
	if _, err := gate.Write(diagnostic); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("uncommitted output = %q", output.String())
	}
	if err := gate.finish(false); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("discarded output = %q", output.String())
	}
}

func TestPayloadOutputGateCommitsVerbatimOutput(t *testing.T) {
	var output bytes.Buffer
	gate := newPayloadOutputGate()
	if err := gate.attach(&output); err != nil {
		t.Fatal(err)
	}

	payload := []byte("<3>bwrap: payload-owned output\n")
	if _, err := gate.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := gate.commit(); err != nil {
		t.Fatal(err)
	}
	if got := output.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("committed output = %q, want %q", got, payload)
	}

	suffix := []byte("<4>bwrap: another payload line\n")
	if _, err := gate.Write(suffix); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), payload...), suffix...)
	if got := output.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("passthrough output = %q, want %q", got, want)
	}
}

func TestPayloadOutputGateOverflowFlushesAndRejectsReplay(t *testing.T) {
	var output bytes.Buffer
	gate := newPayloadOutputGate()
	if err := gate.attach(&output); err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("x"), maxPrePayloadOutput+1)
	if count, err := gate.Write(payload); err != nil {
		t.Fatal(err)
	} else if count != len(payload) {
		t.Fatalf("write count = %d, want %d", count, len(payload))
	}
	if gate.replayAllowed() {
		t.Fatal("overflowed output remained replay-safe")
	}
	if got := output.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("overflow output length = %d, want %d", len(got), len(payload))
	}
}

func TestPayloadOutputGateReturnsWriterFailureImmediately(t *testing.T) {
	sentinel := errors.New("output failed")
	gate := newPayloadOutputGate()
	if err := gate.attach(errorWriter{err: sentinel}); err != nil {
		t.Fatal(err)
	}
	if err := gate.commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := gate.Write([]byte("payload")); !errors.Is(err, sentinel) {
		t.Fatalf("write error = %v, want %v", err, sentinel)
	}
}

func TestPayloadOutputGateReportsCommitFailure(t *testing.T) {
	sentinel := errors.New("commit output failed")
	gate := newPayloadOutputGate()
	if err := gate.attach(errorWriter{err: sentinel}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Write([]byte("buffered")); err != nil {
		t.Fatal(err)
	}

	if err := gate.commit(); !errors.Is(err, sentinel) {
		t.Fatalf("commit error = %v, want %v", err, sentinel)
	}
	select {
	case err := <-gate.failures:
		if !errors.Is(err, sentinel) {
			t.Fatalf("reported failure = %v, want %v", err, sentinel)
		}
	default:
		t.Fatal("commit failure was not reported to the executor")
	}
}

func TestRetryOutputPreservesManagedTerminalIdentity(t *testing.T) {
	controller, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer terminal.Close()

	output, err := newRetryAttemptOutput(
		ProcessIO{
			Stdin:  terminal,
			Stdout: terminal,
			Stderr: terminal,
		},
		ExecutionManagedPTY,
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.streams.Stdout != terminal {
		t.Fatalf(
			"managed stdout type = %T, want original *os.File",
			output.streams.Stdout,
		)
	}
	if _, _, interactive := terminalFiles(output.streams); !interactive {
		t.Fatal("retry-authorized managed streams lost terminal identity")
	}

	output.abortPreparation()
	<-output.readyDone
	output.close()
}

func TestRetryOutputPassesDirectTerminalStderrToPayload(t *testing.T) {
	controller, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer terminal.Close()

	output, err := newRetryAttemptOutput(
		ProcessIO{Stderr: terminal},
		ExecutionDirectTerminal,
	)
	if err != nil {
		t.Fatal(err)
	}
	invocation := &Invocation{
		Args:            []string{"--", "/bin/true"},
		payloadArgIndex: 1,
	}
	if err := output.prepare(invocation); err != nil {
		t.Fatal(err)
	}

	readyFD, stderrFD, signalFD, payload, handled := execInvocation(
		invocation.Args[1:],
		"1",
	)
	if !handled {
		t.Fatalf("prepared payload invocation = %q", invocation.Args)
	}
	if readyFD != childExtraFileBaseFD {
		t.Fatalf("ready FD = %d, want %d", readyFD, childExtraFileBaseFD)
	}
	if stderrFD != childExtraFileBaseFD+1 {
		t.Fatalf(
			"stderr FD = %d, want %d",
			stderrFD,
			childExtraFileBaseFD+1,
		)
	}
	if signalFD != -1 {
		t.Fatalf("signal FD = %d, want -1", signalFD)
	}
	if len(payload) != 1 || payload[0] != "/bin/true" {
		t.Fatalf("payload = %q, want [/bin/true]", payload)
	}
	if len(invocation.ExtraFiles) != 2 ||
		!term.IsTerminal(int(invocation.ExtraFiles[1].Fd())) {
		t.Fatal("prepared payload stderr is not the original terminal")
	}

	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
	<-output.readyDone
	output.close()
}

func TestManagedRetryOutputFlushesAfterTerminalForegroundCloses(t *testing.T) {
	var output bytes.Buffer
	sink := managedForegroundOutput{
		foreground: &terminalForeground{closed: true},
		fallback:   &output,
	}
	gate := newPayloadOutputGate()
	if err := gate.attach(sink); err != nil {
		t.Fatal(err)
	}

	diagnostic := []byte("bwrap: final overlay failure\n")
	if _, err := gate.Write(diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := gate.finish(true); err != nil {
		t.Fatal(err)
	}
	if got := output.Bytes(); !bytes.Equal(got, diagnostic) {
		t.Fatalf("final diagnostic = %q, want %q", got, diagnostic)
	}
}

func TestPayloadOutputGateShortWriteFails(t *testing.T) {
	gate := newPayloadOutputGate()
	if err := gate.attach(shortWriter{}); err != nil {
		t.Fatal(err)
	}
	if err := gate.commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := gate.Write([]byte("payload")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short-write error = %v, want %v", err, io.ErrShortWrite)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	return max(0, len(data)-1), nil
}
