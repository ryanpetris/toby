package status

// Renders Rich-style byte progress bars and measurement columns.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	ociReferenceDisplayWidth  = 36
	ociReferenceOmittedPrefix = ".../"
	ociActionDisplayWidth     = 10
)

var (
	progressBarActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("45"))
	progressBarFinishedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("42"))
	progressBarEmptyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))
	progressPercentageStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("13"))
	progressCompletedSizeStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("45"))
	progressFinishedSizeStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("42"))
	progressTotalSizeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
	progressMeasurementSeparatorStyle = lipgloss.NewStyle().
						Foreground(lipgloss.Color("240"))
)

func renderRichProgressBar(
	percent float64,
	width int,
	finished bool,
) string {
	if width <= 0 {
		return ""
	}
	percent = min(max(percent, 0), 1)

	completeHalves := int(float64(width*2) * percent)
	completeCharacters := completeHalves / 2
	hasHalfCharacter := completeHalves%2 != 0

	completeStyle := progressBarActiveStyle
	if finished {
		completeStyle = progressBarFinishedStyle
	}

	var bar strings.Builder
	if completeCharacters > 0 {
		bar.WriteString(completeStyle.Render(
			strings.Repeat("━", completeCharacters),
		))
	}
	if hasHalfCharacter {
		bar.WriteString(completeStyle.Render("╸"))
	}

	remaining := width - completeCharacters
	if hasHalfCharacter {
		remaining--
	} else if completeCharacters > 0 && remaining > 0 {
		bar.WriteString(progressBarEmptyStyle.Render("╺"))
		remaining--
	}
	if remaining > 0 {
		bar.WriteString(progressBarEmptyStyle.Render(
			strings.Repeat("━", remaining),
		))
	}

	return bar.String()
}

func renderProgressMeasurement(
	completed int64,
	total int64,
) string {
	values := formatByteProgress(completed, total)
	finished := total > 0 && completed >= total

	percentageStyle := progressPercentageStyle
	completedStyle := progressCompletedSizeStyle
	if finished {
		percentageStyle = progressFinishedSizeStyle
		completedStyle = progressFinishedSizeStyle
	}

	percentage := 0.0
	if total > 0 {
		percentage = float64(completed) / float64(total) * 100
	}
	return percentageStyle.Render(
		fmt.Sprintf("%3.0f%%", percentage),
	) + " " +
		completedStyle.Render(values.completed) +
		progressMeasurementSeparatorStyle.Render("/") +
		progressTotalSizeStyle.Render(values.total+" "+values.unit)
}

func formatBytePair(completed int64, total int64) string {
	values := formatByteProgress(completed, total)
	return values.completed + "/" + values.total + " " + values.unit
}

func renderOCIReferenceColumn(reference string) string {
	reference = shortenOCIReference(
		reference,
		ociReferenceDisplayWidth,
	)
	padding := ociReferenceDisplayWidth - ansi.StringWidth(reference)
	if padding > 0 {
		reference += strings.Repeat(" ", padding)
	}
	return reference
}

func renderOCIActionColumn(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "Preparing"
	}
	action = ansi.Truncate(action, ociActionDisplayWidth, "")
	padding := ociActionDisplayWidth - ansi.StringWidth(action)
	if padding > 0 {
		action += strings.Repeat(" ", padding)
	}
	return action
}

func shortenOCIReference(reference string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(reference) <= width {
		return reference
	}

	segments := strings.Split(reference, "/")
	for index := 1; index < len(segments); index++ {
		candidate := ociReferenceOmittedPrefix +
			strings.Join(segments[index:], "/")
		if ansi.StringWidth(candidate) <= width {
			return candidate
		}
	}

	prefix := ""
	if len(segments) > 1 {
		prefix = ociReferenceOmittedPrefix
	}
	available := width - ansi.StringWidth(prefix)
	if available <= 0 {
		return ansi.Truncate(prefix, width, "")
	}
	return prefix + ansi.Truncate(
		segments[len(segments)-1],
		available,
		"...",
	)
}

type formattedByteProgress struct {
	completed string
	total     string
	unit      string
}

func formatByteProgress(
	completed int64,
	total int64,
) formattedByteProgress {
	completed = max(completed, 0)
	total = max(total, 0)
	scaleFrom := max(completed, total)

	const base = int64(1024)
	units := [...]string{
		"B",
		"KiB",
		"MiB",
		"GiB",
		"TiB",
		"PiB",
		"EiB",
	}
	divisor := int64(1)
	unitIndex := 0
	for unitIndex < len(units)-1 && scaleFrom >= divisor*base {
		divisor *= base
		unitIndex++
	}

	formatValue := func(value int64) string {
		if unitIndex == 0 {
			return fmt.Sprintf("%d", value)
		}
		return fmt.Sprintf("%.1f", float64(value)/float64(divisor))
	}
	return formattedByteProgress{
		completed: formatValue(completed),
		total:     formatValue(total),
		unit:      units[unitIndex],
	}
}
