package cli

// Covers the Bubble Tea volume table and removal decision model.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"petris.dev/toby/internal/storage"
)

func TestVolumeRemovalModelConfirmsAndCancels(t *testing.T) {
	volumes := []storage.VolumeInfo{{
		ID:      strings.Repeat("a", 128),
		Type:    storage.VolumeTypeHome,
		Name:    "workspace",
		Profile: "default",
	}}

	for _, test := range []struct {
		key       rune
		confirmed bool
	}{
		{key: 'y', confirmed: true},
		{key: 'n', confirmed: false},
	} {
		model := newVolumeRemovalModel(volumes)
		updated, command := model.Update(tea.KeyPressMsg{
			Code: test.key,
			Text: string(test.key),
		})
		got := updated.(volumeRemovalModel)
		if !got.decided || got.confirmed != test.confirmed {
			t.Fatalf(
				"key %q produced %#v",
				test.key,
				got,
			)
		}
		if command == nil {
			t.Fatalf("key %q did not quit", test.key)
		}
	}
}

func TestVolumeRemovalModelShowsMatchedMetadata(t *testing.T) {
	model := newVolumeRemovalModel([]storage.VolumeInfo{{
		ID:      strings.Repeat("b", 128),
		Type:    storage.VolumeTypeTool,
		Name:    "opencode",
		Profile: "work",
		Purpose: "data",
	}})
	view := model.View().Content
	for _, want := range []string{
		"bbbbbbbbbbbb",
		"tool",
		"opencode",
		"work",
		"data",
		"Remove 1 volume?",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation view %q does not contain %q", view, want)
		}
	}
}

func TestVolumeRemovalProgressShowsOnlyTheActiveRow(t *testing.T) {
	volumes := []storage.VolumeInfo{
		{
			ID:      strings.Repeat("a", 128),
			Type:    storage.VolumeTypeHome,
			Name:    "workspace",
			Profile: "work",
		},
		{
			ID:      strings.Repeat("b", 128),
			Type:    storage.VolumeTypeTool,
			Name:    "opencode",
			Profile: "default",
			Purpose: "config",
		},
	}
	model := newVolumeRemovalProgressModel(
		make(chan struct{}),
		volumes,
	)
	update := func(progress storage.VolumeRemovalProgress) {
		updated, _ := model.Update(volumeRemovalProgressMessage{
			progress: progress,
		})
		model = updated.(volumeRemovalProgressModel)
	}

	update(storage.VolumeRemovalProgress{
		ID:    volumes[0].ID,
		Phase: storage.VolumeRemovalPhaseRemoving,
	})
	if view := model.View().Content; !strings.Contains(
		view,
		"⠋ Removing home workspace (work)",
	) {
		t.Fatalf("running view = %q", view)
	}

	update(storage.VolumeRemovalProgress{
		ID:    volumes[0].ID,
		Phase: storage.VolumeRemovalPhaseRemoved,
	})
	if view := model.View().Content; view != "" {
		t.Fatalf("completed row remained in the live view: %q", view)
	}

	update(storage.VolumeRemovalProgress{
		ID:    volumes[1].ID,
		Phase: storage.VolumeRemovalPhaseRemoving,
	})
	view := model.View().Content
	if !strings.Contains(
		view,
		"⠋ Removing tool opencode/config (default)",
	) || strings.Contains(view, "workspace") {
		t.Fatalf("active progress view = %q", view)
	}

	update(storage.VolumeRemovalProgress{
		ID:    volumes[1].ID,
		Phase: storage.VolumeRemovalPhaseRemoved,
	})
	if view := model.View().Content; view != "" {
		t.Fatalf("completed row remained in the live view: %q", view)
	}
}

func TestVolumeRemovalProgressEscapesTerminalControls(t *testing.T) {
	volume := storage.VolumeInfo{
		ID:      strings.Repeat("c", 128),
		Type:    storage.VolumeTypeHome,
		Name:    "unsafe\x1b[2J",
		Profile: "default",
	}
	row := renderVolumeRemovalProgressRow(
		volumeRemovalProgressRow{
			volume: volume,
			phase:  storage.VolumeRemovalPhaseRemoving,
		},
		0,
		80,
	)
	if strings.Contains(row, "\x1b") ||
		!strings.Contains(row, `unsafe\x1b[2J`) {
		t.Fatalf("unsafe removal row = %q", row)
	}
}
