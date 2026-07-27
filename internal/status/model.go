package status

// Bubble Tea model for bounded concurrent startup operations rendered inline.

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	// SpinnerFrameInterval is the shared cadence for Toby's inline activity
	// indicators.
	SpinnerFrameInterval         = 100 * time.Millisecond
	maximumInlineTranscriptLines = 10
	minimumProgressBarWidth      = 8
)

var progressFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SpinnerFrame returns one frame from Toby's standard inline spinner.
func SpinnerFrame(frame int) string {
	if frame < 0 {
		frame = 0
	}
	return progressFrames[frame%len(progressFrames)]
}

var operationOutputStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("245"))

type progressRow struct {
	Scope       string
	Step        string
	Running     bool
	Failed      bool
	HasProgress bool
	Progress    Progress
}

type progressUpdate struct {
	Rows       []progressRow
	Transcript string
}

type progressTick struct{}

type progressStop struct{}

type progressModel struct {
	ready      chan struct{}
	rows       []progressRow
	transcript string
	width      int
	height     int
	frame      int
	stopped    bool
}

var _ tea.Model = progressModel{}

func newProgressModel(ready chan struct{}) progressModel {
	return progressModel{ready: ready}
}

func (m progressModel) Init() tea.Cmd {
	close(m.ready)
	return progressTickCommand()
}

func (m progressModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
	case progressUpdate:
		m.rows = append(m.rows[:0], message.Rows...)
		m.transcript = message.Transcript
	case progressTick:
		m.frame = (m.frame + 1) % len(progressFrames)
		return m, progressTickCommand()
	case progressStop:
		m.stopped = true
		m.rows = nil
		m.transcript = ""
		return m, tea.Quit
	}

	return m, nil
}

func (m progressModel) View() tea.View {
	if m.stopped {
		return tea.NewView("")
	}

	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	lines := visibleProgressLines(m.rows, m.frame, width, height)

	available := min(
		height-len(lines),
		maximumInlineTranscriptLines,
	)
	body := visibleTranscript(m.transcript, width, available)
	if body != "" {
		lines = append(lines, renderOperationOutput(body))
	}

	return tea.NewView(strings.Join(lines, "\n"))
}

func visibleProgressLines(
	rows []progressRow,
	frame int,
	width int,
	height int,
) []string {
	if height <= 0 {
		return nil
	}

	blocks := renderProgressBlocks(rows, frame, width)

	remaining := height
	selected := make([][]string, 0, len(blocks))
	for index := len(blocks) - 1; index >= 0; index-- {
		block := blocks[index]
		if len(block) > remaining {
			if remaining > 0 && len(selected) == 0 {
				clipped := []string{block[0]}
				if remaining > 1 {
					clipped = append(
						clipped,
						block[len(block)-(remaining-1):]...,
					)
				}
				selected = append(selected, clipped)
				remaining = 0
			}
			continue
		}
		selected = append(selected, block)
		remaining -= len(block)
	}
	if len(selected) == 0 && len(blocks) > 0 {
		return blocks[len(blocks)-1][:1]
	}

	lines := make([]string, 0, height-remaining)
	for index := len(selected) - 1; index >= 0; index-- {
		lines = append(lines, selected[index]...)
	}
	return lines
}

func renderProgressBlocks(
	rows []progressRow,
	frame int,
	width int,
) [][]string {
	var ociRows []progressRow
	firstOCI := -1
	for index, row := range rows {
		if row.HasProgress && row.Progress.OCIReference != "" {
			if firstOCI < 0 {
				firstOCI = index
			}
			ociRows = append(ociRows, row)
		}
	}

	blocks := make([][]string, 0, len(rows))
	for index, row := range rows {
		if index == firstOCI {
			blocks = append(
				blocks,
				renderOCIProgressBlock(ociRows, frame, width),
			)
		}
		if row.HasProgress && row.Progress.OCIReference != "" {
			continue
		}
		blocks = append(blocks, []string{
			renderProgressRow(row, frame, width),
		})
	}
	return blocks
}

func renderOCIProgressBlock(
	rows []progressRow,
	frame int,
	width int,
) []string {
	statusRow := progressRow{
		Step: "Preparing OCI images",
	}
	if len(rows) == 1 {
		statusRow.Step = "Preparing OCI image"
	}
	for _, row := range rows {
		statusRow.Running = statusRow.Running || row.Running
		statusRow.Failed = statusRow.Failed || row.Failed
	}

	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, renderProgressRow(statusRow, frame, width))
	for _, row := range rows {
		lines = append(
			lines,
			renderOCIProgressRow(row, width),
		)
	}
	return lines
}

func renderProgressRow(
	row progressRow,
	frame int,
	width int,
) string {
	marker := "✓"
	switch {
	case row.Running:
		marker = SpinnerFrame(frame)
	case row.Failed:
		marker = "✗"
	}
	prefix := marker + " " + operationLabel(row.Scope, row.Step)
	if !row.HasProgress || row.Progress.TotalBytes == 0 {
		return ansi.Truncate(prefix, width, "")
	}

	completed := min(
		row.Progress.CompletedBytes,
		row.Progress.TotalBytes,
	)
	percent := float64(completed) / float64(row.Progress.TotalBytes)
	suffix := renderProgressMeasurement(
		completed,
		row.Progress.TotalBytes,
	)
	barWidth := width -
		ansi.StringWidth(prefix) -
		ansi.StringWidth(suffix) -
		2
	if barWidth < minimumProgressBarWidth {
		prefixWidth := width - ansi.StringWidth(suffix) - 1
		if prefixWidth <= 0 {
			return ansi.Truncate(suffix, width, "")
		}
		return ansi.Truncate(prefix, prefixWidth, "") + " " + suffix
	}

	bar := renderRichProgressBar(
		percent,
		barWidth,
		completed == row.Progress.TotalBytes,
	)
	return ansi.Truncate(
		prefix+" "+bar+" "+suffix,
		width,
		"",
	)
}

func renderOCIProgressRow(row progressRow, width int) string {
	const indentation = "  "

	progress := row.Progress
	reference := renderOCIReferenceColumn(progress.OCIReference)
	actionName := progress.OCIAction
	if row.Failed {
		actionName = "Failed"
	}
	action := renderOCIActionColumn(actionName)
	prefix := indentation + reference + "  " + action
	if progress.TotalBytes == 0 {
		return ansi.Truncate(prefix, width, "")
	}

	completed := min(progress.CompletedBytes, progress.TotalBytes)
	percent := float64(completed) / float64(progress.TotalBytes)
	suffix := renderProgressMeasurement(completed, progress.TotalBytes)
	barWidth := width -
		ansi.StringWidth(prefix) -
		ansi.StringWidth(suffix) -
		2
	if barWidth < minimumProgressBarWidth {
		prefixWidth := width - ansi.StringWidth(suffix) - 1
		if prefixWidth <= 0 {
			return ansi.Truncate(suffix, width, "")
		}
		return ansi.Truncate(prefix, prefixWidth, "") + " " + suffix
	}

	bar := renderRichProgressBar(
		percent,
		barWidth,
		completed == progress.TotalBytes,
	)
	return ansi.Truncate(
		prefix+" "+bar+" "+suffix,
		width,
		"",
	)
}

func renderOperationOutput(output string) string {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		lines[index] = operationOutputStyle.Render(line)
	}
	return strings.Join(lines, "\n")
}

func progressTickCommand() tea.Cmd {
	return tea.Tick(SpinnerFrameInterval, func(time.Time) tea.Msg {
		return progressTick{}
	})
}

func visibleTranscript(transcript string, width, height int) string {
	if transcript == "" || height <= 0 {
		return ""
	}

	wrapped := ansi.Wrap(strings.TrimRight(transcript, "\n"), width, "")
	lines := strings.Split(wrapped, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}
