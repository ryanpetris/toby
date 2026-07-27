package executable

// Guards host and sandbox-helper startup against host-root execution.

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

const procSelfUIDMap = "/proc/self/uid_map"

var errRootExecution = errors.New("toby must not run as root")

type uidMapReader func(string) ([]byte, error)

// CheckUnprivileged rejects an effective UID of zero.
func CheckUnprivileged() error {
	return checkUnprivileged(os.Geteuid())
}

func checkUnprivileged(effectiveUID int) error {
	if effectiveUID == 0 {
		return errRootExecution
	}

	return nil
}

// CheckSandboxUnprivileged rejects host root while permitting Bubblewrap
// namespace root that maps to an unprivileged parent UID.
func CheckSandboxUnprivileged() error {
	return checkSandboxUnprivileged(os.Geteuid(), os.ReadFile)
}

func checkSandboxUnprivileged(
	effectiveUID int,
	readUIDMap uidMapReader,
) error {
	if effectiveUID != 0 {
		return nil
	}

	data, err := readUIDMap(procSelfUIDMap)
	if err != nil {
		return errRootExecution
	}
	parentUID, found := mappedParentUID(data, uint64(effectiveUID))
	if !found || parentUID == 0 {
		return errRootExecution
	}

	return nil
}

func mappedParentUID(data []byte, namespaceUID uint64) (uint64, bool) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return 0, false
		}

		namespaceStart, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		parentStart, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		length, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil || length == 0 {
			return 0, false
		}
		if namespaceUID < namespaceStart {
			continue
		}

		offset := namespaceUID - namespaceStart
		if offset >= length {
			continue
		}
		if parentStart > ^uint64(0)-offset {
			return 0, false
		}

		return parentStart + offset, true
	}
	if scanner.Err() != nil {
		return 0, false
	}

	return 0, false
}
