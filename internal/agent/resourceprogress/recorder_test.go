//go:build linux

package resourceprogress

// Exercises exact progress persistence, terminal state, and bounded truncation.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/config"
)

func TestRecorderPersistsProgressAndTerminalState(t *testing.T) {
	logs := testLogs(t)
	recorder := New(logs, nil, protocol.ResourceMCP, "resource")
	if err := recorder.Report(protocol.AcquireProgress{
		Sequence:  1,
		Operation: "backend",
		Kind:      protocol.ProgressStep,
		Source:    "mcp",
		Text:      "starting",
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish(nil); err != nil {
		t.Fatal(err)
	}

	records := readRecords(t, logs)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0].Kind != "progress" ||
		records[0].Progress == nil ||
		records[0].Progress.Text != "starting" {
		t.Fatalf("progress record = %#v", records[0])
	}
	if records[1].Kind != "complete" {
		t.Fatalf("terminal record = %#v", records[1])
	}
	if records[0].GenerationID != records[1].GenerationID ||
		records[0].Sequence != 1 ||
		records[1].Sequence != 2 {
		t.Fatalf("record sequence = %#v", records)
	}
}

func TestRecorderBoundsEveryProgressShape(t *testing.T) {
	logs := testLogs(t)
	recorder := New(logs, nil, protocol.ResourceMCP, "resource")
	recorder.maxBytes = 1

	for range 3 {
		if err := recorder.Report(protocol.AcquireProgress{
			Kind:   protocol.ProgressStep,
			Source: "mcp",
			Text:   "startup step",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Finish(context.Canceled); err != nil {
		t.Fatal(err)
	}

	records := readRecords(t, logs)
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	if records[0].Kind != "truncated" ||
		records[1].Kind != "failed" {
		t.Fatalf("records = %#v", records)
	}
}

func testLogs(t *testing.T) *resourcelog.Service {
	t.Helper()
	root := t.TempDir()
	return resourcelog.NewService(config.Paths{
		Home:         root,
		XDGCacheHome: filepath.Join(root, "cache"),
	}, nil)
}

func readRecords(
	t *testing.T,
	logs *resourcelog.Service,
) []record {
	t.Helper()
	file, _, err := logs.Open(
		protocol.ResourceMCP,
		"resource",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var result []record
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var item record
			if decodeErr := json.Unmarshal(line, &item); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			result = append(result, item)
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			t.Fatal(err)
		}
		return result
	}
}
