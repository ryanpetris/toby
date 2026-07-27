package status

// Verifies Rich-style progress geometry, styling, and byte columns.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRichProgressBarUsesHalfSegmentsAndDistinctStyles(t *testing.T) {
	bar := renderRichProgressBar(0.5, 8, false)
	if got := ansi.Strip(bar); got != "━━━━╺━━━" {
		t.Fatalf("half-complete bar = %q", got)
	}
	if bar == ansi.Strip(bar) {
		t.Fatal("progress bar has no terminal styling")
	}

	bar = renderRichProgressBar(0.5625, 8, false)
	if got := ansi.Strip(bar); got != "━━━━╸━━━" {
		t.Fatalf("half-segment bar = %q", got)
	}

	bar = renderRichProgressBar(1, 8, true)
	if got := ansi.Strip(bar); got != "━━━━━━━━" {
		t.Fatalf("finished bar = %q", got)
	}
}

func TestProgressMeasurementUsesOneCommonBinaryUnit(t *testing.T) {
	measurement := renderProgressMeasurement(256<<20, 512<<20)
	if got := ansi.Strip(measurement); got != " 50% 256.0/512.0 MiB" {
		t.Fatalf("measurement = %q", got)
	}
	if measurement == ansi.Strip(measurement) {
		t.Fatal("progress measurement has no terminal styling")
	}

	maximum := int64(^uint64(0) >> 1)
	if got := formatBytePair(maximum, maximum); got != "8.0/8.0 EiB" {
		t.Fatalf("maximum byte pair = %q", got)
	}

	plain := plainProgressLine("Pulling example", Progress{
		CompletedBytes: 256 << 20,
		TotalBytes:     512 << 20,
	})
	if plain != "Pulling example: 50% 256.0/512.0 MiB" {
		t.Fatalf("plain progress = %q", plain)
	}
}

func TestRichProgressBarClampsOutOfRangeValues(t *testing.T) {
	for _, test := range []struct {
		percent float64
		want    string
	}{
		{percent: -1, want: "━━━━"},
		{percent: 2, want: "━━━━"},
	} {
		got := ansi.Strip(renderRichProgressBar(
			test.percent,
			4,
			test.percent >= 1,
		))
		if got != test.want || strings.Count(got, "━") != 4 {
			t.Fatalf("bar at %v = %q, want %q", test.percent, got, test.want)
		}
	}
}

func TestShortenOCIReferenceDropsWholeLeftSegmentsFirst(t *testing.T) {
	reference := "registry.example/foo/bar/baz:latest"
	for _, test := range []struct {
		width int
		want  string
	}{
		{width: 40, want: reference},
		{width: 21, want: ".../bar/baz:latest"},
		{width: 16, want: ".../baz:latest"},
		{width: 13, want: ".../baz:la..."},
	} {
		got := shortenOCIReference(reference, test.width)
		if got != test.want {
			t.Fatalf(
				"shorten width %d = %q, want %q",
				test.width,
				got,
				test.want,
			)
		}
		if width := ansi.StringWidth(got); width > test.width {
			t.Fatalf(
				"shortened width = %d, limit = %d: %q",
				width,
				test.width,
				got,
			)
		}
	}
}

func TestOCIReferenceColumnHasFixedDisplayWidth(t *testing.T) {
	for _, reference := range []string{
		"docker.io/library/caddy:latest",
		"mcr.microsoft.com/devcontainers/javascript-node:24-bookworm",
	} {
		column := renderOCIReferenceColumn(reference)
		if width := ansi.StringWidth(column); width !=
			ociReferenceDisplayWidth {
			t.Fatalf(
				"reference column width = %d, want %d: %q",
				width,
				ociReferenceDisplayWidth,
				column,
			)
		}
	}
}

func TestOCIActionColumnHasFixedDisplayWidth(t *testing.T) {
	for _, action := range []string{"Pulling", "Extracting"} {
		column := renderOCIActionColumn(action)
		if width := ansi.StringWidth(column); width !=
			ociActionDisplayWidth {
			t.Fatalf(
				"action column width = %d, want %d: %q",
				width,
				ociActionDisplayWidth,
				column,
			)
		}
	}
}
