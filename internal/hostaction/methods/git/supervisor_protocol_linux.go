//go:build linux

package git

// Defines the bounded private protocol between the launch process and its
// exact-binary Git supervisor.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const (
	gitSupervisorArgument        = "__toby_internal_git_supervisor_v1"
	gitSupervisorProtocol        = 1
	gitSupervisorRepositoryFD    = 3
	gitSupervisorLifetimeFD      = 4
	gitSupervisorStatusFD        = 5
	gitSupervisorArgumentsFD     = 6
	maxGitSupervisorArguments    = 1024
	maxGitSupervisorArgumentSize = 1 << 20
	maxGitSupervisorStatusSize   = 4 << 10
	maxGitSupervisorErrorSize    = 2 << 10
)

const gitSupervisorArgumentSeals = unix.F_SEAL_SHRINK |
	unix.F_SEAL_GROW |
	unix.F_SEAL_WRITE |
	unix.F_SEAL_SEAL

type gitSupervisorArguments struct {
	Version int      `json:"version"`
	Command []string `json:"command"`
}

type gitSupervisorFailure string

const (
	gitSupervisorFailureNone       gitSupervisorFailure = ""
	gitSupervisorFailureNotFound   gitSupervisorFailure = "not_found"
	gitSupervisorFailurePermission gitSupervisorFailure = "permission"
	gitSupervisorFailureInternal   gitSupervisorFailure = "internal"
)

type gitSupervisorStatus struct {
	Version  int                  `json:"version"`
	Started  bool                 `json:"started"`
	Canceled bool                 `json:"canceled,omitempty"`
	ExitCode int                  `json:"exitCode"`
	Failure  gitSupervisorFailure `json:"failure,omitempty"`
	Error    string               `json:"error,omitempty"`
}

func createGitSupervisorArguments(
	command []string,
) (result *os.File, returnErr error) {
	arguments := gitSupervisorArguments{
		Version: gitSupervisorProtocol,
		Command: append([]string(nil), command...),
	}
	if err := validateGitSupervisorArguments(arguments); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("encode Git supervisor arguments: %w", err)
	}
	defer clear(payload)
	if len(payload) > maxGitSupervisorArgumentSize {
		return nil, fmt.Errorf(
			"git supervisor argument payload exceeds %d bytes",
			maxGitSupervisorArgumentSize,
		)
	}

	flags := unix.MFD_CLOEXEC |
		unix.MFD_ALLOW_SEALING |
		unix.MFD_NOEXEC_SEAL
	descriptor, err := unix.MemfdCreate("toby-git-arguments", flags)
	if errors.Is(err, unix.EINVAL) {
		descriptor, err = unix.MemfdCreate(
			"toby-git-arguments",
			unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("create Git supervisor argument memfd: %w", err)
	}

	file := os.NewFile(uintptr(descriptor), "toby-git-arguments")
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"Git supervisor argument preparation already failed",
				"close incomplete Git supervisor arguments",
				file.Close(),
			)
			result = nil
		}
	}()

	if err := writeAllGitSupervisor(file, payload); err != nil {
		return nil, fmt.Errorf("write Git supervisor arguments: %w", err)
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, fmt.Errorf(
			"make Git supervisor arguments non-executable: %w",
			err,
		)
	}

	requiredSeals := gitSupervisorArgumentSeals | unix.F_SEAL_EXEC
	if _, err := unix.FcntlInt(
		file.Fd(),
		unix.F_ADD_SEALS,
		requiredSeals,
	); errors.Is(err, unix.EINVAL) {
		requiredSeals = gitSupervisorArgumentSeals
		_, err = unix.FcntlInt(
			file.Fd(),
			unix.F_ADD_SEALS,
			requiredSeals,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"seal Git supervisor arguments: %w",
				err,
			)
		}
	} else if err != nil {
		return nil, fmt.Errorf("seal Git supervisor arguments: %w", err)
	}

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return nil, fmt.Errorf("verify Git supervisor argument seals: %w", err)
	}
	if seals&requiredSeals != requiredSeals {
		return nil, fmt.Errorf(
			"git supervisor argument seals %#x omit required %#x",
			seals,
			requiredSeals,
		)
	}

	return file, nil
}

func readGitSupervisorArguments(file *os.File) (gitSupervisorArguments, error) {
	if file == nil {
		return gitSupervisorArguments{}, fmt.Errorf(
			"git supervisor argument file is nil",
		)
	}

	info, err := file.Stat()
	if err != nil {
		return gitSupervisorArguments{}, fmt.Errorf(
			"inspect Git supervisor arguments: %w",
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return gitSupervisorArguments{}, fmt.Errorf(
			"git supervisor arguments are not a regular file",
		)
	}
	if info.Size() <= 0 || info.Size() > maxGitSupervisorArgumentSize {
		return gitSupervisorArguments{}, fmt.Errorf(
			"git supervisor argument size %d is outside 1..%d",
			info.Size(),
			maxGitSupervisorArgumentSize,
		)
	}

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return gitSupervisorArguments{}, fmt.Errorf(
			"inspect Git supervisor argument seals: %w",
			err,
		)
	}
	if seals&gitSupervisorArgumentSeals != gitSupervisorArgumentSeals {
		return gitSupervisorArguments{}, fmt.Errorf(
			"git supervisor argument seals %#x omit required %#x",
			seals,
			gitSupervisorArgumentSeals,
		)
	}

	payload := make([]byte, int(info.Size()))
	count, err := file.ReadAt(payload, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		clear(payload)
		return gitSupervisorArguments{}, fmt.Errorf(
			"read Git supervisor arguments: %w",
			err,
		)
	}
	if count != len(payload) {
		clear(payload)
		return gitSupervisorArguments{}, fmt.Errorf(
			"read %d of %d Git supervisor argument bytes",
			count,
			len(payload),
		)
	}
	defer clear(payload)

	var arguments gitSupervisorArguments
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&arguments); err != nil {
		return gitSupervisorArguments{}, fmt.Errorf(
			"decode Git supervisor arguments: %w",
			err,
		)
	}
	if err := requireGitSupervisorJSONEnd(decoder); err != nil {
		return gitSupervisorArguments{}, err
	}
	if err := validateGitSupervisorArguments(arguments); err != nil {
		return gitSupervisorArguments{}, err
	}

	return arguments, nil
}

func validateGitSupervisorArguments(arguments gitSupervisorArguments) error {
	if arguments.Version != gitSupervisorProtocol {
		return fmt.Errorf(
			"git supervisor argument version %d is unsupported",
			arguments.Version,
		)
	}
	if len(arguments.Command) == 0 ||
		len(arguments.Command) > maxGitSupervisorArguments {
		return fmt.Errorf(
			"git supervisor command count %d is outside 1..%d",
			len(arguments.Command),
			maxGitSupervisorArguments,
		)
	}
	if arguments.Command[0] != "git" {
		return fmt.Errorf(
			"git supervisor executable must be %q",
			"git",
		)
	}
	for index, argument := range arguments.Command {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf(
				"git supervisor argument %d contains a NUL byte",
				index,
			)
		}
	}

	return nil
}

func writeGitSupervisorStatus(
	file *os.File,
	status gitSupervisorStatus,
) error {
	if file == nil {
		return fmt.Errorf("git supervisor status file is nil")
	}
	status.Version = gitSupervisorProtocol
	if len(status.Error) > maxGitSupervisorErrorSize {
		status.Error = status.Error[:maxGitSupervisorErrorSize]
	}
	if err := validateGitSupervisorStatus(status); err != nil {
		return err
	}

	payload, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode Git supervisor status: %w", err)
	}
	if len(payload) > maxGitSupervisorStatusSize {
		return fmt.Errorf(
			"git supervisor status exceeds %d bytes",
			maxGitSupervisorStatusSize,
		)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate Git supervisor status: %w", err)
	}
	count, err := file.WriteAt(payload, 0)
	if err != nil {
		return fmt.Errorf("write Git supervisor status: %w", err)
	}
	if count != len(payload) {
		return fmt.Errorf(
			"write %d of %d Git supervisor status bytes",
			count,
			len(payload),
		)
	}

	return nil
}

func readGitSupervisorStatus(file *os.File) (gitSupervisorStatus, error) {
	if file == nil {
		return gitSupervisorStatus{}, fmt.Errorf(
			"git supervisor status file is nil",
		)
	}

	info, err := file.Stat()
	if err != nil {
		return gitSupervisorStatus{}, fmt.Errorf(
			"inspect Git supervisor status: %w",
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return gitSupervisorStatus{}, fmt.Errorf(
			"git supervisor status is not a regular file",
		)
	}
	if info.Size() <= 0 || info.Size() > maxGitSupervisorStatusSize {
		return gitSupervisorStatus{}, fmt.Errorf(
			"git supervisor status size %d is outside 1..%d",
			info.Size(),
			maxGitSupervisorStatusSize,
		)
	}

	payload := make([]byte, int(info.Size()))
	count, err := file.ReadAt(payload, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return gitSupervisorStatus{}, fmt.Errorf(
			"read Git supervisor status: %w",
			err,
		)
	}
	if count != len(payload) {
		return gitSupervisorStatus{}, fmt.Errorf(
			"read %d of %d Git supervisor status bytes",
			count,
			len(payload),
		)
	}

	var status gitSupervisorStatus
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return gitSupervisorStatus{}, fmt.Errorf(
			"decode Git supervisor status: %w",
			err,
		)
	}
	if err := requireGitSupervisorJSONEnd(decoder); err != nil {
		return gitSupervisorStatus{}, err
	}
	if err := validateGitSupervisorStatus(status); err != nil {
		return gitSupervisorStatus{}, err
	}

	return status, nil
}

func validateGitSupervisorStatus(status gitSupervisorStatus) error {
	if status.Version != gitSupervisorProtocol {
		return fmt.Errorf(
			"git supervisor status version %d is unsupported",
			status.Version,
		)
	}
	if status.ExitCode < 0 || status.ExitCode > 255 {
		return fmt.Errorf(
			"git supervisor exit code %d is outside 0..255",
			status.ExitCode,
		)
	}
	switch status.Failure {
	case gitSupervisorFailureNone:
		if status.Error != "" {
			return fmt.Errorf(
				"successful Git supervisor status contains an error",
			)
		}
	case gitSupervisorFailureNotFound,
		gitSupervisorFailurePermission,
		gitSupervisorFailureInternal:
		if status.Error == "" {
			return fmt.Errorf(
				"failed Git supervisor status omits its error",
			)
		}
	default:
		return fmt.Errorf(
			"git supervisor failure %q is unsupported",
			status.Failure,
		)
	}
	if status.Started && status.Failure == gitSupervisorFailureNotFound {
		return fmt.Errorf(
			"started Git supervisor reports executable not found",
		)
	}

	return nil
}

func requireGitSupervisorJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("git supervisor JSON has trailing content")
		}
		return fmt.Errorf("decode trailing Git supervisor JSON: %w", err)
	}

	return nil
}

func writeAllGitSupervisor(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		count, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
		payload = payload[count:]
	}

	return nil
}
