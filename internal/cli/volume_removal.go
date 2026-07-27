package cli

// Presents inline progress while deleting persistent volumes.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/storage"
)

const volumeRemovalDefaultWidth = 80

type volumeRemovalProgressMessage struct {
	progress storage.VolumeRemovalProgress
}

type volumeRemovalTick struct{}

type volumeRemovalDone struct{}

type volumeRemovalProgressRow struct {
	volume storage.VolumeInfo
	phase  storage.VolumeRemovalPhase
}

type volumeRemovalProgressModel struct {
	ready   chan struct{}
	volumes map[string]storage.VolumeInfo
	rows    []volumeRemovalProgressRow
	indexes map[string]int
	width   int
	frame   int
}

var _ tea.Model = volumeRemovalProgressModel{}

func newVolumeRemovalProgressModel(
	ready chan struct{},
	volumes []storage.VolumeInfo,
) volumeRemovalProgressModel {
	byID := make(map[string]storage.VolumeInfo, len(volumes))
	for _, volume := range volumes {
		byID[volume.ID] = volume
	}
	return volumeRemovalProgressModel{
		ready:   ready,
		volumes: byID,
		indexes: make(map[string]int, len(volumes)),
	}
}

func (m volumeRemovalProgressModel) Init() tea.Cmd {
	close(m.ready)
	return volumeRemovalTickCommand()
}

func (m volumeRemovalProgressModel) Update(
	message tea.Msg,
) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
	case volumeRemovalProgressMessage:
		progress := message.progress
		index, found := m.indexes[progress.ID]
		if !found {
			if progress.Phase == storage.VolumeRemovalPhaseRemoved ||
				progress.Phase == storage.VolumeRemovalPhaseFailed {
				break
			}
			volume, known := m.volumes[progress.ID]
			if !known {
				volume.ID = progress.ID
			}
			m.rows = append(m.rows, volumeRemovalProgressRow{
				volume: volume,
				phase:  progress.Phase,
			})
			m.indexes[progress.ID] = len(m.rows) - 1
			break
		}
		switch progress.Phase {
		case storage.VolumeRemovalPhaseRemoved,
			storage.VolumeRemovalPhaseFailed:
			m.rows = append(m.rows[:index], m.rows[index+1:]...)
			delete(m.indexes, progress.ID)
			for rowIndex := index; rowIndex < len(m.rows); rowIndex++ {
				m.indexes[m.rows[rowIndex].volume.ID] = rowIndex
			}
		default:
			m.rows[index].phase = progress.Phase
		}
	case volumeRemovalTick:
		m.frame++
		return m, volumeRemovalTickCommand()
	case volumeRemovalDone:
		return m, tea.Quit
	}

	return m, nil
}

func (m volumeRemovalProgressModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = volumeRemovalDefaultWidth
	}

	lines := make([]string, 0, len(m.rows))
	for _, row := range m.rows {
		lines = append(
			lines,
			renderVolumeRemovalProgressRow(row, m.frame, width),
		)
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func renderVolumeRemovalProgressRow(
	row volumeRemovalProgressRow,
	frame int,
	width int,
) string {
	marker := status.SpinnerFrame(frame)
	action := "Removing"
	switch row.phase {
	case storage.VolumeRemovalPhaseRemoved:
		marker = "✓"
		action = "Removed"
	case storage.VolumeRemovalPhaseFailed:
		marker = "✗"
		action = "Failed to remove"
	}

	line := fmt.Sprintf(
		"%s %s %s",
		marker,
		action,
		volumeRemovalDescription(row.volume),
	)
	return ansi.Truncate(line, width, "")
}

func volumeRemovalDescription(volume storage.VolumeInfo) string {
	name := tableCell(volume.Name)
	profile := tableCell(volume.Profile)
	switch volume.Type {
	case storage.VolumeTypeHome:
		return fmt.Sprintf("home %s (%s)", name, profile)
	case storage.VolumeTypeTool:
		purpose := tableCell(volume.Purpose)
		return fmt.Sprintf(
			"tool %s/%s (%s)",
			name,
			purpose,
			profile,
		)
	default:
		return "volume " + volume.ShortID()
	}
}

func volumeRemovalTickCommand() tea.Cmd {
	return tea.Tick(status.SpinnerFrameInterval, func(time.Time) tea.Msg {
		return volumeRemovalTick{}
	})
}

func runVolumeRemovalProgress(
	ctx context.Context,
	output io.Writer,
	store *storage.Store,
	volumes []storage.VolumeInfo,
	logger *diagnostic.Logger,
) error {
	ready := make(chan struct{})
	model := newVolumeRemovalProgressModel(ready, volumes)
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(output),
		tea.WithoutSignalHandler(),
	)
	programEnd := make(chan error, 1)
	go func() {
		_, err := program.Run()
		programEnd <- err
	}()

	select {
	case <-ready:
	case err := <-programEnd:
		if err == nil {
			err = fmt.Errorf("progress display stopped before removal")
		}
		logger.DebugError("start volume removal progress", err)
		return removeVolumesPlain(ctx, output, store, volumes)
	}

	ids := make([]string, 0, len(volumes))
	volumesByID := make(map[string]storage.VolumeInfo, len(volumes))
	for _, volume := range volumes {
		ids = append(ids, volume.ID)
		volumesByID[volume.ID] = volume
	}
	width := terminalStreamWidth(output)
	if width <= 0 {
		width = volumeRemovalDefaultWidth
	}
	_, removeErr := store.RemoveVolumes(
		ctx,
		ids,
		func(progress storage.VolumeRemovalProgress) {
			if progress.Phase == storage.VolumeRemovalPhaseRemoved ||
				progress.Phase == storage.VolumeRemovalPhaseFailed {
				row := volumeRemovalProgressRow{
					volume: volumesByID[progress.ID],
					phase:  progress.Phase,
				}
				printLine := tea.Println(
					renderVolumeRemovalProgressRow(row, 0, width),
				)
				program.Send(printLine())
			}
			program.Send(volumeRemovalProgressMessage{
				progress: progress,
			})
		},
	)

	program.Send(volumeRemovalDone{})
	programErr := <-programEnd
	logger.DebugError("render volume removal progress", programErr)
	return removeErr
}

func removeVolumesPlain(
	ctx context.Context,
	output io.Writer,
	store *storage.Store,
	volumes []storage.VolumeInfo,
) error {
	ids := make([]string, 0, len(volumes))
	for _, volume := range volumes {
		ids = append(ids, volume.ID)
	}

	removed, removeErr := store.RemoveVolumes(ctx, ids, nil)
	var outputErr error
	for _, id := range removed {
		if _, err := fmt.Fprintln(output, id); err != nil {
			outputErr = errors.Join(outputErr, err)
		}
	}
	return errors.Join(removeErr, outputErr)
}
