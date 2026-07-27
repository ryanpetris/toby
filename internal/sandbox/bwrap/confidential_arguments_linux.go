//go:build linux

package bwrap

// Encodes confidential background-service arguments in sealed anonymous files
// so secrets never enter Bubblewrap's observable process argument vector.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

const (
	maxConfidentialArgumentPayloadSize = 1 << 20
	// Bubblewrap 0.11.2 permits at most 9000 total outer and file-supplied
	// arguments. Executor contributes the six outer --json-status-fd,
	// --block-fd, and --args tokens.
	maxConfidentialArgumentCount = 9000 - 6
)

const confidentialArgumentRequiredSeals = unix.F_SEAL_SHRINK |
	unix.F_SEAL_GROW |
	unix.F_SEAL_WRITE |
	unix.F_SEAL_SEAL

// setConfidentialOptions retains Bubblewrap setup options in a sealed
// anonymous file while leaving the explicitly public payload command after
// --args. Bubblewrap 0.11.2 accepts only options, not the payload command, from
// a nested --args source.
func (i *Invocation) setConfidentialOptions(
	options []string,
	publicCommand []string,
) error {
	if len(publicCommand) == 0 {
		return fmt.Errorf("public Bubblewrap command must not be empty")
	}
	for _, option := range options {
		if option == "--" {
			return fmt.Errorf(
				"confidential Bubblewrap options must not contain a command separator",
			)
		}
	}
	for index, argument := range publicCommand {
		if (index == 0 && argument == "") ||
			strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf(
				"public Bubblewrap command argument %d is invalid",
				index,
			)
		}
	}
	if len(options) > maxConfidentialArgumentCount-
		len(publicCommand)-1 {
		return fmt.Errorf(
			"confidential Bubblewrap argument count %d with public command count %d exceeds %d",
			len(options),
			len(publicCommand),
			maxConfidentialArgumentCount,
		)
	}

	tail := make([]string, 0, len(publicCommand)+1)
	tail = append(tail, "--")
	tail = append(tail, publicCommand...)

	return i.setConfidentialArgumentsWithTail(options, tail)
}

func (i *Invocation) setConfidentialArgumentsWithTail(
	args []string,
	publicTail []string,
) error {
	if i == nil {
		return fmt.Errorf("bubblewrap invocation is nil")
	}
	if i.confidentialArguments || len(i.Args) != 0 {
		return fmt.Errorf("bubblewrap invocation arguments are already set")
	}

	payload, err := encodeConfidentialArguments(args)
	if err != nil {
		return err
	}
	defer clear(payload)

	file, err := createConfidentialArgumentFile(payload)
	if err != nil {
		return err
	}

	index := len(i.ExtraFiles)
	childFD := childExtraFileBaseFD + index
	i.ExtraFiles = append(i.ExtraFiles, file)
	i.Args = make([]string, 0, len(publicTail)+2)
	i.Args = append(i.Args, "--args", strconv.Itoa(childFD))
	i.Args = append(i.Args, publicTail...)
	i.confidentialArguments = true
	i.confidentialArgumentsIndex = index

	return nil
}

func invocationArguments(invocation *Invocation) ([]string, error) {
	if invocation == nil {
		return nil, fmt.Errorf("bubblewrap invocation is nil")
	}
	if !invocation.confidentialArguments {
		return append([]string(nil), invocation.Args...), nil
	}
	if err := validateConfidentialArgumentReference(invocation); err != nil {
		return nil, err
	}

	payload, err := readConfidentialArgumentFile(
		invocation.ExtraFiles[invocation.confidentialArgumentsIndex],
	)
	if err != nil {
		return nil, err
	}
	defer clear(payload)

	args, err := decodeConfidentialArguments(payload)
	if err != nil {
		return nil, err
	}

	return append(args, invocation.Args[2:]...), nil
}

func validateConfidentialArgumentReference(invocation *Invocation) error {
	if invocation == nil || !invocation.confidentialArguments {
		return nil
	}

	index := invocation.confidentialArgumentsIndex
	if index < 0 || index >= len(invocation.ExtraFiles) {
		return fmt.Errorf(
			"confidential Bubblewrap argument descriptor index %d is outside the descriptor table",
			index,
		)
	}
	if invocation.ExtraFiles[index] == nil {
		return fmt.Errorf("confidential Bubblewrap argument descriptor is nil")
	}

	childFD := childExtraFileBaseFD + index
	if len(invocation.Args) < 2 ||
		invocation.Args[0] != "--args" ||
		invocation.Args[1] != strconv.Itoa(childFD) {
		return fmt.Errorf(
			"confidential Bubblewrap arguments must use --args descriptor %d",
			childFD,
		)
	}
	if len(invocation.Args) > 2 &&
		(len(invocation.Args) < 4 ||
			invocation.Args[2] != "--" ||
			invocation.Args[3] == "") {
		return fmt.Errorf(
			"confidential Bubblewrap arguments have an invalid public command tail",
		)
	}

	return nil
}

func duplicateConfidentialArgumentFile(source *os.File) (*os.File, error) {
	payload, err := readConfidentialArgumentFile(source)
	if err != nil {
		return nil, fmt.Errorf(
			"read confidential Bubblewrap argument descriptor: %w",
			err,
		)
	}
	defer clear(payload)

	file, err := createConfidentialArgumentFile(payload)
	if err != nil {
		return nil, fmt.Errorf(
			"duplicate confidential Bubblewrap argument descriptor: %w",
			err,
		)
	}

	return file, nil
}

func encodeConfidentialArguments(args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf(
			"confidential Bubblewrap arguments must not be empty",
		)
	}
	if len(args) > maxConfidentialArgumentCount {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument count %d exceeds %d",
			len(args),
			maxConfidentialArgumentCount,
		)
	}

	payload := make([]byte, 0, min(
		maxConfidentialArgumentPayloadSize,
		len(args)*16,
	))
	for index, argument := range args {
		if strings.IndexByte(argument, 0) >= 0 {
			clear(payload)
			return nil, fmt.Errorf(
				"confidential Bubblewrap argument %d contains a NUL byte",
				index,
			)
		}
		if len(argument)+1 >
			maxConfidentialArgumentPayloadSize-len(payload) {
			clear(payload)
			return nil, fmt.Errorf(
				"confidential Bubblewrap argument payload exceeds %d bytes",
				maxConfidentialArgumentPayloadSize,
			)
		}

		payload = append(payload, argument...)
		payload = append(payload, 0)
	}

	return payload, nil
}

func decodeConfidentialArguments(payload []byte) ([]string, error) {
	if err := validateConfidentialArgumentPayload(payload); err != nil {
		return nil, err
	}

	count := bytes.Count(payload, []byte{0})
	args := make([]string, 0, count)
	start := 0
	for index, value := range payload {
		if value != 0 {
			continue
		}
		args = append(args, string(payload[start:index]))
		start = index + 1
	}

	return args, nil
}

func validateConfidentialArgumentPayload(payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("confidential Bubblewrap argument payload is empty")
	}
	if len(payload) > maxConfidentialArgumentPayloadSize {
		return fmt.Errorf(
			"confidential Bubblewrap argument payload size %d exceeds %d",
			len(payload),
			maxConfidentialArgumentPayloadSize,
		)
	}
	if payload[len(payload)-1] != 0 {
		return fmt.Errorf(
			"confidential Bubblewrap argument payload is not NUL terminated",
		)
	}

	count := bytes.Count(payload, []byte{0})
	if count > maxConfidentialArgumentCount {
		return fmt.Errorf(
			"confidential Bubblewrap argument count %d exceeds %d",
			count,
			maxConfidentialArgumentCount,
		)
	}

	return nil
}

func readConfidentialArgumentFile(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument descriptor is nil",
		)
	}

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf(
			"inspect confidential Bubblewrap argument descriptor: %w",
			err,
		)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument descriptor is not a regular anonymous file",
		)
	}
	if info.Mode().Perm()&0o111 != 0 {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument descriptor is executable",
		)
	}
	if info.Size() <= 0 ||
		info.Size() > maxConfidentialArgumentPayloadSize {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument payload size %d is outside 1..%d bytes",
			info.Size(),
			maxConfidentialArgumentPayloadSize,
		)
	}

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return nil, fmt.Errorf(
			"inspect confidential Bubblewrap argument seals: %w",
			err,
		)
	}
	if seals&confidentialArgumentRequiredSeals !=
		confidentialArgumentRequiredSeals {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument seals %#x omit required %#x",
			seals,
			confidentialArgumentRequiredSeals,
		)
	}

	payload := make([]byte, int(info.Size()))
	count, err := file.ReadAt(payload, 0)
	if err != nil {
		clear(payload)
		return nil, fmt.Errorf(
			"read confidential Bubblewrap argument payload: %w",
			err,
		)
	}
	if count != len(payload) {
		clear(payload)
		return nil, fmt.Errorf(
			"read confidential Bubblewrap argument payload: read %d of %d bytes",
			count,
			len(payload),
		)
	}
	if err := validateConfidentialArgumentPayload(payload); err != nil {
		clear(payload)
		return nil, err
	}

	return payload, nil
}

func createConfidentialArgumentFile(
	payload []byte,
) (result *os.File, returnErr error) {
	if err := validateConfidentialArgumentPayload(payload); err != nil {
		return nil, err
	}

	flags := unix.MFD_CLOEXEC |
		unix.MFD_ALLOW_SEALING |
		unix.MFD_NOEXEC_SEAL
	descriptor, err := unix.MemfdCreate("toby-bubblewrap-args", flags)
	if errors.Is(err, unix.EINVAL) {
		descriptor, err = unix.MemfdCreate(
			"toby-bubblewrap-args",
			unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING,
		)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"create confidential Bubblewrap argument memfd: %w",
			err,
		)
	}

	file := os.NewFile(uintptr(descriptor), "toby-bubblewrap-args")
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"closing an unpublished argument file cannot change the preparation failure",
				"close confidential Bubblewrap argument file",
				file.Close(),
			)
			result = nil
		}
	}()

	for written := 0; written < len(payload); {
		count, err := file.Write(payload[written:])
		if err != nil {
			return nil, fmt.Errorf(
				"write confidential Bubblewrap argument memfd: %w",
				err,
			)
		}
		if count == 0 {
			return nil, fmt.Errorf(
				"write confidential Bubblewrap argument memfd: no progress",
			)
		}
		written += count
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, fmt.Errorf(
			"make confidential Bubblewrap argument memfd non-executable: %w",
			err,
		)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf(
			"rewind confidential Bubblewrap argument memfd: %w",
			err,
		)
	}

	requiredSeals := confidentialArgumentRequiredSeals | unix.F_SEAL_EXEC
	if _, err := unix.FcntlInt(
		file.Fd(),
		unix.F_ADD_SEALS,
		requiredSeals,
	); errors.Is(err, unix.EINVAL) {
		requiredSeals = confidentialArgumentRequiredSeals
		_, err = unix.FcntlInt(
			file.Fd(),
			unix.F_ADD_SEALS,
			requiredSeals,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"seal confidential Bubblewrap argument memfd: %w",
				err,
			)
		}
	} else if err != nil {
		return nil, fmt.Errorf(
			"seal confidential Bubblewrap argument memfd: %w",
			err,
		)
	}

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return nil, fmt.Errorf(
			"verify confidential Bubblewrap argument memfd seals: %w",
			err,
		)
	}
	if seals&requiredSeals != requiredSeals {
		return nil, fmt.Errorf(
			"confidential Bubblewrap argument memfd seals %#x omit required %#x",
			seals,
			requiredSeals,
		)
	}

	return file, nil
}
