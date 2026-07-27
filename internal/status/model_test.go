package status

// Verifies bounded inline layout, wrapping, and the empty final frame used
// before foreground handoff.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestProgressModelRendersBoundedWrappedInlineView(t *testing.T) {
	model := progressModel{
		transcript: strings.Repeat("a long startup output line\n", 20),
		width:      16,
		height:     40,
		rows: []progressRow{{
			Step:    "Preparing a deliberately long operation",
			Running: true,
		}},
	}

	view := model.View()
	if view.AltScreen {
		t.Fatal("progress model requested the alternate screen")
	}
	if strings.HasPrefix(view.Content, "Toby") {
		t.Fatalf("inline view contains a redundant header: %q", view.Content)
	}

	lines := strings.Split(view.Content, "\n")
	maximumLines := 1 + maximumInlineTranscriptLines
	if len(lines) > maximumLines {
		t.Fatalf(
			"inline view has %d lines, want at most %d",
			len(lines),
			maximumLines,
		)
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > model.width {
			t.Fatalf(
				"inline line width = %d, terminal width = %d: %q",
				width,
				model.width,
				line,
			)
		}
	}
}

func TestProgressModelStylesOnlyOperationOutput(t *testing.T) {
	model := progressModel{
		transcript: "checking tool\ninstalling tool\n",
		width:      80,
		height:     24,
		rows: []progressRow{{
			Scope: "OpenCode",
			Step:  "Installing",
		}},
	}

	statusLine, output, found := strings.Cut(model.View().Content, "\n")
	if !found {
		t.Fatal("inline view does not contain operation output")
	}
	if statusLine != "✓ OpenCode: Installing" {
		t.Fatalf("status line = %q, want unstyled status text", statusLine)
	}

	wantOutput := renderOperationOutput("checking tool\ninstalling tool")
	if output != wantOutput {
		t.Fatalf("operation output = %q, want %q", output, wantOutput)
	}
	if output == ansi.Strip(output) {
		t.Fatal("operation output does not contain terminal styling")
	}
	if got := ansi.Strip(output); got != "checking tool\ninstalling tool" {
		t.Fatalf("visible operation output = %q", got)
	}
}

func TestProgressModelStopsWithEmptyFinalFrame(t *testing.T) {
	model := progressModel{
		transcript: "startup output\n",
		width:      80,
		height:     24,
		rows: []progressRow{{
			Step:    "Preparing",
			Running: true,
		}},
	}

	updated, command := model.Update(progressStop{})
	if command == nil {
		t.Fatal("progress stop did not request program exit")
	}
	stopped, ok := updated.(progressModel)
	if !ok {
		t.Fatalf("updated model has type %T", updated)
	}
	if got := stopped.View().Content; got != "" {
		t.Fatalf("final frame = %q, want empty", got)
	}
}

func TestProgressModelRendersConcurrentProgressRows(t *testing.T) {
	model := progressModel{
		width:  100,
		height: 24,
		rows: []progressRow{
			{
				Step:        "Pulling OCI image example.test/one:latest",
				Running:     true,
				HasProgress: true,
				Progress: Progress{
					CompletedBytes: 50,
					TotalBytes:     100,
					OCIAction:      "Pulling",
					OCIReference:   "example.test/one:latest",
				},
			},
			{
				Step:        "Extracting OCI image example.test/two:latest",
				Running:     true,
				HasProgress: true,
				Progress: Progress{
					CompletedBytes: 75,
					TotalBytes:     100,
					OCIAction:      "Extracting",
					OCIReference:   "example.test/two:latest",
				},
			},
		},
	}

	content := ansi.Strip(model.View().Content)
	lines := strings.Split(content, "\n")
	if len(lines) != 3 {
		t.Fatalf("progress lines = %d, want 3: %q", len(lines), content)
	}
	if lines[0] != "⠋ Preparing OCI images" ||
		!strings.Contains(lines[1], "example.test/one:latest") ||
		!strings.Contains(lines[1], "Pulling") ||
		!strings.Contains(lines[1], "50%") ||
		!strings.Contains(lines[2], "example.test/two:latest") ||
		!strings.Contains(lines[2], "Extracting") ||
		!strings.Contains(lines[2], "75%") {
		t.Fatalf("concurrent progress = %q", content)
	}
	if !strings.Contains(lines[1], "━") ||
		!strings.Contains(lines[2], "━") {
		t.Fatalf("concurrent progress omitted Rich-style bars: %q", content)
	}
}

func TestProgressModelFitsRichOCIColumnsAtEightyColumns(t *testing.T) {
	reference := "mcr.microsoft.com/devcontainers/" +
		"javascript-node:24-bookworm"
	row := progressRow{
		Step:        "Extracting OCI image " + reference,
		Running:     true,
		HasProgress: true,
		Progress: Progress{
			CompletedBytes: 256 << 20,
			TotalBytes:     512 << 20,
			OCIAction:      "Extracting",
			OCIReference:   reference,
		},
	}

	block := renderOCIProgressBlock([]progressRow{row}, 0, 80)
	if len(block) != 2 {
		t.Fatalf("progress block = %#v", block)
	}
	content := ansi.Strip(strings.Join(block, "\n"))
	for _, want := range []string{
		"⠋ Preparing OCI image",
		".../javascript-node:24-bookworm",
		"Extracting",
		"━",
		"50%",
		"256.0/512.0 MiB",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("progress row %q does not contain %q", content, want)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("progress width = %d: %q", width, line)
		}
	}
}

func TestProgressModelKeepsOCIStatusAndTransferLinesTogether(t *testing.T) {
	rows := []progressRow{
		{
			Step:        "Pulling OCI image example.test/old:latest",
			Running:     true,
			HasProgress: true,
			Progress: Progress{
				CompletedBytes: 1,
				TotalBytes:     2,
				OCIAction:      "Pulling",
				OCIReference:   "example.test/old:latest",
			},
		},
		{
			Step:        "Extracting OCI image example.test/new:latest",
			Running:     true,
			HasProgress: true,
			Progress: Progress{
				CompletedBytes: 1,
				TotalBytes:     2,
				OCIAction:      "Extracting",
				OCIReference:   "example.test/new:latest",
			},
		},
	}

	content := ansi.Strip(strings.Join(
		visibleProgressLines(rows, 0, 80, 2),
		"\n",
	))
	if strings.Contains(content, "old:latest") ||
		!strings.Contains(content, "⠋ Preparing OCI images") ||
		!strings.Contains(content, "example.test/new:latest") {
		t.Fatalf("bounded progress lines split an OCI block: %q", content)
	}
}

func TestProgressModelPreservesProgressAtNarrowWidths(t *testing.T) {
	row := progressRow{
		Step: "Pulling OCI image mcr.microsoft.com/devcontainers/" +
			"javascript-node:24-bookworm",
		Running:     true,
		HasProgress: true,
		Progress: Progress{
			CompletedBytes: 256 << 20,
			TotalBytes:     512 << 20,
		},
	}

	content := ansi.Strip(renderProgressRow(row, 0, 48))
	if width := ansi.StringWidth(content); width > 48 {
		t.Fatalf("progress width = %d: %q", width, content)
	}
	if !strings.Contains(content, "50%") ||
		!strings.Contains(content, "256.0/512.0 MiB") {
		t.Fatalf("narrow progress omitted its measurement: %q", content)
	}
}
