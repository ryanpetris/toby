//go:build linux

package bwrap

// Verifies exact internal payload dispatch, ready signaling, descriptor
// closure, and exec argument preservation.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if code, handled := DispatchExec(
		os.Args,
		os.Getenv("TOBY_SANDBOX"),
		os.Stderr,
	); handled {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestPayloadInvocationRecognizesExactSandboxShape(t *testing.T) {
	readyFD, stderrFD, signalFD, payload, handled := execInvocation(
		[]string{
			"/toby/bin/tobys",
			"exec",
			"9",
			"10",
			"11",
			"--",
			"/bin/tool",
			"--flag",
		},
		"1",
	)
	if !handled {
		t.Fatal("payload invocation was not handled")
	}
	if readyFD != 9 {
		t.Fatalf("ready FD = %d, want 9", readyFD)
	}
	if stderrFD != 10 {
		t.Fatalf("stderr FD = %d, want 10", stderrFD)
	}
	if signalFD != 11 {
		t.Fatalf("signal FD = %d, want 11", signalFD)
	}
	if want := []string{"/bin/tool", "--flag"}; !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %q, want %q", payload, want)
	}
}

func TestPayloadInvocationRejectsUntrustedShapes(t *testing.T) {
	tests := [][]string{
		{"/toby/bin/tobys", "exec", "2", "-1", "11", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "not-fd", "-1", "11", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "bad-fd", "11", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "9", "11", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "-1", "bad-fd", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "-1", "9", "--", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "-1", "11", "/bin/true"},
		{"/toby/bin/tobys", "exec", "9", "-1", "11", "--"},
	}
	for _, arguments := range tests {
		if _, _, _, _, handled := execInvocation(arguments, "1"); handled {
			t.Errorf("invalid payload invocation was handled: %q", arguments)
		}
	}
	if _, _, _, _, handled := execInvocation(
		[]string{
			"/toby/bin/tobys",
			"exec",
			"9",
			"-1",
			"11",
			"--",
			"/bin/true",
		},
		"",
	); handled {
		t.Fatal("host payload invocation was handled")
	}
}

func TestExecutePayloadSignalsReadyAndPreservesExecContract(t *testing.T) {
	descriptors := []int{-1, -1}
	if err := unix.Pipe2(descriptors, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(descriptors[0]), "payload-ready reader")
	if reader == nil {
		_ = unix.Close(descriptors[0])
		_ = unix.Close(descriptors[1])
		t.Fatal("payload-ready reader descriptor is invalid")
	}
	defer reader.Close()
	signalReader, signalWriter := payloadSignalSocketPair(t)
	defer signalReader.Close()
	signalFD, err := unix.Dup(int(signalWriter.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := signalWriter.Close(); err != nil {
		if closeErr := unix.Close(signalFD); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal(err)
	}

	sentinel := errors.New("exec intercepted")
	payload := []string{"/bin/sh", "one", "--", "two"}
	environment := []string{"A=B", "C=D"}
	var gotPath string
	var gotArguments []string
	var gotEnvironment []string

	code, err := executePayload(
		descriptors[1],
		-1,
		signalFD,
		payload,
		environment,
		func(path string, arguments, currentEnvironment []string) error {
			gotPath = path
			gotArguments = append([]string(nil), arguments...)
			gotEnvironment = append(
				[]string(nil),
				currentEnvironment...,
			)
			return sentinel
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("execute error = %v, want %v", err, sentinel)
	}
	if code != payloadCannotInvokeCode {
		t.Fatalf("execute code = %d, want %d", code, payloadCannotInvokeCode)
	}
	assertPayloadProcessDescriptor(t, signalReader)

	marker, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if want := []byte{payloadReadyByte}; !reflect.DeepEqual(marker, want) {
		t.Fatalf("ready marker = %v, want %v", marker, want)
	}
	if gotPath != "/bin/sh" {
		t.Fatalf("exec path = %q, want %q", gotPath, "/bin/sh")
	}
	if !reflect.DeepEqual(gotArguments, payload) {
		t.Fatalf("exec arguments = %q, want %q", gotArguments, payload)
	}
	if !reflect.DeepEqual(gotEnvironment, environment) {
		t.Fatalf(
			"exec environment = %q, want %q",
			gotEnvironment,
			environment,
		)
	}
}

func TestPayloadDispatchSubprocessExitAndPATHSemantics(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join(bin, "relative-tool")
	if err := os.WriteFile(
		relative,
		[]byte("#!/bin/sh\nexit 23\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	nonExecutable := filepath.Join(bin, "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plainScript := filepath.Join(bin, "plain-script")
	if err := os.WriteFile(
		plainScript,
		[]byte("exit 29\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		payload string
		code    int
	}{
		{
			name:    "relative PATH entry",
			payload: "relative-tool",
			code:    23,
		},
		{
			name:    "missing command",
			payload: "definitely-missing-toby-payload",
			code:    payloadNotFoundCode,
		},
		{
			name:    "non-executable command",
			payload: "not-executable",
			code:    payloadCannotInvokeCode,
		},
		{
			name:    "executable text fallback",
			payload: "plain-script",
			code:    29,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, marker, _ := runPayloadDispatchSubprocess(
				t,
				root,
				[]string{"TOBY_SANDBOX=1", "PATH=bin"},
				-1,
				nil,
				test.payload,
			)
			if code != test.code {
				t.Fatalf("exit code = %d, want %d", code, test.code)
			}
			if want := []byte{payloadReadyByte}; !bytes.Equal(marker, want) {
				t.Fatalf("ready marker = %v, want %v", marker, want)
			}
		})
	}
}

func TestPayloadDispatchRestoresTerminalStderrBeforeReady(t *testing.T) {
	controller, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	defer terminal.Close()

	code, marker, stderr := runPayloadDispatchSubprocess(
		t,
		"",
		[]string{"TOBY_SANDBOX=1", "PATH=/bin:/usr/bin"},
		childExtraFileBaseFD+1,
		terminal,
		"/bin/sh",
		"-c",
		"test -t 2",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; shim stderr = %q", code, stderr)
	}
	if want := []byte{payloadReadyByte}; !bytes.Equal(marker, want) {
		t.Fatalf("ready marker = %v, want %v", marker, want)
	}
}

func runPayloadDispatchSubprocess(
	t *testing.T,
	directory string,
	environment []string,
	stderrFD int,
	payloadStderr *os.File,
	payload ...string,
) (int, []byte, string) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	signalReader, signalWriter := payloadSignalSocketPair(t)
	defer signalReader.Close()

	extraFiles := []*os.File{writer}
	if payloadStderr != nil {
		extraFiles = append(extraFiles, payloadStderr)
	}
	signalFD := childExtraFileBaseFD + len(extraFiles)
	extraFiles = append(extraFiles, signalWriter)

	arguments := []string{
		"exec",
		strconv.Itoa(childExtraFileBaseFD),
		strconv.Itoa(stderrFD),
		strconv.Itoa(signalFD),
		"--",
	}
	arguments = append(arguments, payload...)
	command := exec.Command(os.Args[0], arguments...)
	command.Dir = directory
	command.Env = environment
	command.ExtraFiles = extraFiles
	var stderr bytes.Buffer
	command.Stderr = &stderr

	runErr := command.Run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := signalWriter.Close(); err != nil {
		t.Fatal(err)
	}
	assertPayloadProcessDescriptor(t, signalReader)
	marker, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr == nil {
		return 0, marker, stderr.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("dispatch subprocess: %v", runErr)
	}

	return exitErr.ExitCode(), marker, stderr.String()
}

func payloadSignalSocketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	descriptors, err := unix.Socketpair(
		unix.AF_UNIX,
		unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	reader := os.NewFile(uintptr(descriptors[0]), "payload-signal reader")
	writer := os.NewFile(uintptr(descriptors[1]), "payload-signal writer")
	if reader == nil || writer == nil {
		if reader != nil {
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		} else if err := unix.Close(descriptors[0]); err != nil {
			t.Error(err)
		}
		if writer != nil {
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
		} else if err := unix.Close(descriptors[1]); err != nil {
			t.Error(err)
		}
		t.Fatal("payload-signal socket file is invalid")
	}

	return reader, writer
}

func assertPayloadProcessDescriptor(t *testing.T, reader *os.File) {
	t.Helper()

	pidfd, received, err := receivePayloadPIDFD(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !received {
		t.Fatal("payload process descriptor was not received")
	}
	if err := unix.PidfdSendSignal(
		pidfd,
		0,
		nil,
		0,
	); err != nil && !errors.Is(err, unix.ESRCH) {
		if closeErr := unix.Close(pidfd); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatalf("validate payload process descriptor: %v", err)
	}
	if err := unix.Close(pidfd); err != nil {
		t.Fatal(err)
	}
}
