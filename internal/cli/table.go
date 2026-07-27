package cli

// Provides shared safe-cell, sizing, and terminal helpers for management
// tables.

import (
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/table"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

func tableCell(value string) string {
	if value == "" {
		return "-"
	}
	if strings.IndexFunc(value, func(character rune) bool {
		return !unicode.IsPrint(character)
	}) >= 0 {
		return strconv.QuoteToGraphic(value)
	}
	return value
}

func tableColumnWidth(
	rows []table.Row,
	column int,
	minimum int,
	maximum int,
) int {
	width := minimum
	for _, row := range rows {
		if column >= len(row) {
			continue
		}
		width = max(width, ansi.StringWidth(row[column]))
	}
	return min(width, maximum)
}

func terminalStream(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func terminalStreamWidth(stream any) int {
	file, ok := stream.(interface{ Fd() uintptr })
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 0
	}
	return width
}
