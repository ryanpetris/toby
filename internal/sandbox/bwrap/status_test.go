package bwrap

// Verifies strict, bounded interpretation of Bubblewrap's payload-status
// protocol.

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadBubblewrapStatusDistinguishesExecFromPreExecFailure(
	t *testing.T,
) {
	executed, err := readBubblewrapStatusEvents(strings.NewReader(
		"{\"child-pid\":42,\"mnt-namespace\":99}\n"+
			"{\"exit-code\":23}\n",
	), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !executed.hasChildPID ||
		executed.childPID != 42 ||
		executed.mntNamespace != 99 ||
		!executed.hasExitCode ||
		executed.exitCode != 23 {
		t.Fatalf("executed status = %#v", executed)
	}

	preExec, err := readBubblewrapStatusEvents(strings.NewReader(
		"{\"child-pid\":43,\"mnt-namespace\":100}\n",
	), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !preExec.hasChildPID ||
		preExec.childPID != 43 ||
		preExec.mntNamespace != 100 ||
		preExec.hasExitCode {
		t.Fatalf("pre-exec status = %#v", preExec)
	}
}

func TestReadBubblewrapStatusPublishesChildBeforeStreamClose(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	child := make(chan bubblewrapChildStatus, 1)
	type result struct {
		status bubblewrapStatus
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		status, err := readBubblewrapStatusEvents(
			reader,
			func(status bubblewrapChildStatus) {
				child <- status
			},
		)
		finished <- result{status: status, err: err}
	}()

	if _, err := io.WriteString(
		writer,
		"{\"child-pid\":42,\"mnt-namespace\":99}\n",
	); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-child:
		if status.pid != 42 || status.mntNamespace != 99 {
			t.Fatalf("published child status = %#v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("child PID was not published while status remained open")
	}

	if _, err := io.WriteString(
		writer,
		"{\"exit-code\":0}\n",
	); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded := <-finished
	if decoded.err != nil {
		t.Fatal(decoded.err)
	}
	if !decoded.status.hasExitCode || decoded.status.exitCode != 0 {
		t.Fatalf("completed status = %#v", decoded.status)
	}
}

func TestReadBubblewrapStatusRejectsMalformedOrUnboundedStreams(
	t *testing.T,
) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "invalid json", status: "{"},
		{name: "unknown event", status: `{"message":"no event"}`},
		{
			name:   "both events",
			status: `{"child-pid":42,"exit-code":0}`,
		},
		{name: "exit first", status: `{"exit-code":0}`},
		{
			name: "duplicate child",
			status: "{\"child-pid\":42,\"mnt-namespace\":99}\n" +
				"{\"child-pid\":43,\"mnt-namespace\":100}\n",
		},
		{
			name: "duplicate exit",
			status: "{\"child-pid\":42,\"mnt-namespace\":99}\n" +
				"{\"exit-code\":0}\n" +
				"{\"exit-code\":0}\n",
		},
		{
			name:   "negative child",
			status: `{"child-pid":-1,"mnt-namespace":99}`,
		},
		{
			name:   "missing mount namespace",
			status: `{"child-pid":42}`,
		},
		{
			name:   "zero mount namespace",
			status: `{"child-pid":42,"mnt-namespace":0}`,
		},
		{
			name: "oversized",
			status: strings.Repeat(
				" ",
				maxBubblewrapStatusBytes+1,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readBubblewrapStatusEvents(
				strings.NewReader(test.status),
				nil,
			); err == nil {
				t.Fatal("invalid status unexpectedly accepted")
			}
		})
	}
}
