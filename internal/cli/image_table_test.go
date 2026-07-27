package cli

// Covers the Bubble Tea image table and removal presentation models.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"petris.dev/toby/internal/oci"
)

func TestImageRemovalModelConfirmsAndCancels(t *testing.T) {
	images := []oci.ImageInfo{testImageInfo()}

	for _, test := range []struct {
		key       rune
		confirmed bool
	}{
		{key: 'y', confirmed: true},
		{key: 'n', confirmed: false},
	} {
		model := newImageRemovalModel(images)
		updated, command := model.Update(tea.KeyPressMsg{
			Code: test.key,
			Text: string(test.key),
		})
		got := updated.(imageRemovalModel)
		if !got.decided || got.confirmed != test.confirmed {
			t.Fatalf("key %q produced %#v", test.key, got)
		}
		if command == nil {
			t.Fatalf("key %q did not quit", test.key)
		}
	}
}

func TestImageRemovalProgressShowsOnlyActiveRows(t *testing.T) {
	first := testImageInfo()
	second := first
	second.ID = strings.Repeat("b", 64)
	second.Reference = "docker.io/library/busybox:latest"
	model := newImageRemovalProgressModel(
		make(chan struct{}),
		[]oci.ImageInfo{first, second},
	)
	update := func(progress oci.ImageRemovalProgress) {
		updated, _ := model.Update(imageRemovalProgressMessage{
			progress: progress,
		})
		model = updated.(imageRemovalProgressModel)
	}

	update(oci.ImageRemovalProgress{
		ID:    first.ID,
		Phase: oci.ImageRemovalPhaseRemoving,
	})
	if view := model.View().Content; !strings.Contains(
		view,
		"Removing docker.io/library/alpine:latest (linux/amd64)",
	) {
		t.Fatalf("running view = %q", view)
	}

	update(oci.ImageRemovalProgress{
		ID:    first.ID,
		Phase: oci.ImageRemovalPhaseUntagged,
	})
	if view := model.View().Content; view != "" {
		t.Fatalf("completed row remained in live view: %q", view)
	}

	update(oci.ImageRemovalProgress{
		ID:    second.ID,
		Phase: oci.ImageRemovalPhaseRemoving,
	})
	view := model.View().Content
	if !strings.Contains(view, "busybox:latest") ||
		strings.Contains(view, "alpine:latest") {
		t.Fatalf("active view = %q", view)
	}
}

func TestImageRemovalProgressEscapesTerminalControls(t *testing.T) {
	image := testImageInfo()
	image.Reference = "example.invalid/unsafe\x1b[2J:latest"
	row := renderImageRemovalProgressRow(
		imageRemovalProgressRow{
			image: image,
			phase: oci.ImageRemovalPhaseRemoving,
		},
		0,
		96,
	)
	if strings.Contains(row, "\x1b") ||
		!strings.Contains(row, `unsafe\x1b[2J`) {
		t.Fatalf("unsafe removal row = %q", row)
	}
}
