//go:build linux

package bwrap

// Dispatches the trusted sandbox exec handoff before replacing the current
// process with the exact requested command.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const (
	payloadCannotInvokeCode = 126
	payloadNotFoundCode     = 127
	payloadStandardErrorFD  = 2
)

type payloadExecFunc func(string, []string, []string) error

// DispatchExec recognizes the internal sandbox exec wrapper before application
// configuration or dependency injection is constructed.
func DispatchExec(
	arguments []string,
	sandboxEnvironment string,
	stderr io.Writer,
) (code int, handled bool) {
	readyFD, stderrFD, signalFD, payload, handled := execInvocation(
		arguments,
		sandboxEnvironment,
	)
	if !handled {
		return 0, false
	}
	if stderr == nil {
		stderr = io.Discard
	}

	code, err := executePayload(
		readyFD,
		stderrFD,
		signalFD,
		payload,
		os.Environ(),
		unix.Exec,
	)
	if err != nil {
		_, writeErr := fmt.Fprintln(stderr, err)
		diagnostic.DiscardError(
			"Fx construction is unavailable",
			"write sandbox payload dispatch error",
			writeErr,
		)
	}

	return code, true
}

func execInvocation(
	arguments []string,
	sandboxEnvironment string,
) (
	readyFD int,
	stderrFD int,
	signalFD int,
	payload []string,
	handled bool,
) {
	if sandboxEnvironment != "1" ||
		len(arguments) < 7 ||
		arguments[1] != "exec" ||
		arguments[5] != "--" {
		return 0, 0, 0, nil, false
	}

	readyFD, err := strconv.Atoi(arguments[2])
	if err != nil ||
		(readyFD != -1 && readyFD < childExtraFileBaseFD) {
		return 0, 0, 0, nil, false
	}
	stderrFD, err = strconv.Atoi(arguments[3])
	if err != nil ||
		(stderrFD != -1 && stderrFD < childExtraFileBaseFD) ||
		(stderrFD >= childExtraFileBaseFD && stderrFD == readyFD) {
		return 0, 0, 0, nil, false
	}
	signalFD, err = strconv.Atoi(arguments[4])
	if err != nil ||
		(signalFD != -1 && signalFD < childExtraFileBaseFD) ||
		(signalFD >= childExtraFileBaseFD &&
			(signalFD == readyFD || signalFD == stderrFD)) {
		return 0, 0, 0, nil, false
	}

	return readyFD,
		stderrFD,
		signalFD,
		append([]string(nil), arguments[6:]...),
		true
}

func executePayload(
	readyFD int,
	stderrFD int,
	signalFD int,
	payload []string,
	environment []string,
	execute payloadExecFunc,
) (int, error) {
	if len(payload) == 0 {
		return payloadCannotInvokeCode, fmt.Errorf(
			"sandbox payload command is empty",
		)
	}
	if execute == nil {
		return payloadCannotInvokeCode, fmt.Errorf(
			"sandbox payload exec function is nil",
		)
	}

	if stderrFD >= childExtraFileBaseFD {
		if err := unix.Dup2(stderrFD, payloadStandardErrorFD); err != nil {
			return payloadCannotInvokeCode, fmt.Errorf(
				"restore sandbox payload stderr: %w",
				err,
			)
		}
		if err := unix.Close(stderrFD); err != nil {
			return payloadCannotInvokeCode, fmt.Errorf(
				"close inherited sandbox payload stderr: %w",
				err,
			)
		}
	}

	if signalFD >= childExtraFileBaseFD {
		if err := publishPayloadPIDFD(signalFD); err != nil {
			return payloadCannotInvokeCode, err
		}
	}

	if readyFD == -1 {
		return executePayloadCommand(payload, environment, execute)
	}
	ready := os.NewFile(uintptr(readyFD), "payload-ready")
	if ready == nil {
		return payloadCannotInvokeCode, fmt.Errorf(
			"payload-ready descriptor %d is invalid",
			readyFD,
		)
	}
	count, writeErr := ready.Write([]byte{payloadReadyByte})
	if writeErr == nil && count != 1 {
		writeErr = io.ErrShortWrite
	}
	closeErr := ready.Close()
	diagnostic.DiscardError(
		"the readiness byte was already written",
		"close payload-ready descriptor",
		closeErr,
	)
	if writeErr != nil {
		return payloadCannotInvokeCode, fmt.Errorf(
			"signal sandbox payload readiness: %w",
			writeErr,
		)
	}

	return executePayloadCommand(payload, environment, execute)
}

func publishPayloadPIDFD(signalFD int) error {
	defer func() {
		diagnostic.DiscardError(
			"the payload process identity was already published",
			"close payload-signal descriptor",
			unix.Close(signalFD),
		)
	}()

	var pidfd int
	var err error
	for {
		pidfd, err = unix.PidfdOpen(os.Getpid(), 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf(
			"retain sandbox payload process: %w",
			err,
		)
	}
	defer func() {
		diagnostic.DiscardError(
			"the payload process descriptor was transferred to the launch client",
			"close sandbox payload process descriptor",
			unix.Close(pidfd),
		)
	}()

	for {
		err = unix.Sendmsg(
			signalFD,
			[]byte{payloadSignalByte},
			unix.UnixRights(pidfd),
			nil,
			0,
		)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf(
			"publish sandbox payload process identity: %w",
			err,
		)
	}

	return nil
}

func executePayloadCommand(
	payload []string,
	environment []string,
	execute payloadExecFunc,
) (int, error) {
	command := payload[0]
	if strings.ContainsRune(command, filepath.Separator) {
		return executePayloadCandidate(
			command,
			payload,
			environment,
			execute,
		)
	}

	searchPath, found := environmentValue(environment, "PATH")
	if !found {
		searchPath = "/bin:/usr/bin"
	}
	directories := filepath.SplitList(searchPath)
	if searchPath == "" {
		directories = []string{""}
	}

	var permissionErr error
	for _, directory := range directories {
		candidate := command
		if directory != "" {
			candidate = filepath.Join(directory, command)
		}

		code, err := executePayloadCandidate(
			candidate,
			payload,
			environment,
			execute,
		)
		if !errors.Is(err, syscall.ENOENT) &&
			!errors.Is(err, syscall.ENOTDIR) &&
			!errors.Is(err, syscall.EACCES) {
			return code, err
		}
		if errors.Is(err, syscall.EACCES) {
			permissionErr = err
		}
	}

	if permissionErr != nil {
		return payloadCannotInvokeCode, fmt.Errorf(
			"exec sandbox payload %q: %w",
			command,
			permissionErr,
		)
	}
	return payloadNotFoundCode, fmt.Errorf(
		"resolve sandbox payload %q: %w",
		command,
		exec.ErrNotFound,
	)
}

func executePayloadCandidate(
	executable string,
	payload []string,
	environment []string,
	execute payloadExecFunc,
) (int, error) {
	err := execute(executable, payload, environment)
	if err == nil {
		return payloadCannotInvokeCode, fmt.Errorf(
			"exec sandbox payload %q returned unexpectedly",
			payload[0],
		)
	}
	if errors.Is(err, syscall.ENOEXEC) {
		shellArguments := make([]string, 0, len(payload)+1)
		shellArguments = append(
			shellArguments,
			"/bin/sh",
			executable,
		)
		shellArguments = append(shellArguments, payload[1:]...)
		err = execute("/bin/sh", shellArguments, environment)
		if err == nil {
			return payloadCannotInvokeCode, fmt.Errorf(
				"exec sandbox payload shell returned unexpectedly",
			)
		}
	}

	code := payloadCannotInvokeCode
	if errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ENOTDIR) {
		code = payloadNotFoundCode
	}
	return code, fmt.Errorf(
		"exec sandbox payload %q: %w",
		payload[0],
		err,
	)
}

func environmentValue(
	environment []string,
	name string,
) (string, bool) {
	prefix := name + "="
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			return strings.TrimPrefix(variable, prefix), true
		}
	}

	return "", false
}
