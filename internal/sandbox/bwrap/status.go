package bwrap

// Strictly decodes Bubblewrap's bounded newline-delimited JSON status stream.

import (
	"encoding/json"
	"fmt"
	"io"
)

const maxBubblewrapStatusBytes = 16 << 10

type bubblewrapStatus struct {
	childPID     int
	mntNamespace uint64
	exitCode     int
	hasChildPID  bool
	hasExitCode  bool
}

type bubblewrapChildStatus struct {
	pid          int
	mntNamespace uint64
}

func readBubblewrapStatusEvents(
	reader io.Reader,
	onChild func(bubblewrapChildStatus),
) (bubblewrapStatus, error) {
	limited := &io.LimitedReader{
		R: reader,
		N: maxBubblewrapStatusBytes + 1,
	}
	decoder := json.NewDecoder(limited)
	var status bubblewrapStatus
	for document := 0; ; document++ {
		var fields map[string]json.RawMessage
		err := decoder.Decode(&fields)
		if err == io.EOF {
			if limited.N == 0 {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status exceeds %d bytes",
					maxBubblewrapStatusBytes,
				)
			}
			return status, nil
		}
		if err != nil {
			if limited.N == 0 {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status exceeds %d bytes",
					maxBubblewrapStatusBytes,
				)
			}
			return bubblewrapStatus{}, fmt.Errorf(
				"decode Bubblewrap JSON status document %d: %w",
				document+1,
				err,
			)
		}

		child, hasChild := fields["child-pid"]
		exit, hasExit := fields["exit-code"]
		if hasChild == hasExit {
			return bubblewrapStatus{}, fmt.Errorf(
				"bubblewrap JSON status document %d must contain exactly one event",
				document+1,
			)
		}

		if hasChild {
			if status.hasChildPID || status.hasExitCode {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status child event is duplicated or out of order",
				)
			}
			if err := json.Unmarshal(child, &status.childPID); err != nil ||
				status.childPID <= 0 {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status has invalid child-pid",
				)
			}
			mntNamespace, hasMntNamespace := fields["mnt-namespace"]
			if !hasMntNamespace {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status has invalid mnt-namespace",
				)
			}
			if err := json.Unmarshal(
				mntNamespace,
				&status.mntNamespace,
			); err != nil ||
				status.mntNamespace == 0 {
				return bubblewrapStatus{}, fmt.Errorf(
					"bubblewrap JSON status has invalid mnt-namespace",
				)
			}
			status.hasChildPID = true
			if onChild != nil {
				onChild(bubblewrapChildStatus{
					pid:          status.childPID,
					mntNamespace: status.mntNamespace,
				})
			}
			continue
		}

		if !status.hasChildPID || status.hasExitCode {
			return bubblewrapStatus{}, fmt.Errorf(
				"bubblewrap JSON status exit event is duplicated or out of order",
			)
		}
		if err := json.Unmarshal(exit, &status.exitCode); err != nil ||
			status.exitCode < 0 ||
			status.exitCode > 255 {
			return bubblewrapStatus{}, fmt.Errorf(
				"bubblewrap JSON status has invalid exit-code",
			)
		}
		status.hasExitCode = true
	}
}
