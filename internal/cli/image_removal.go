package cli

// Presents inline Bubble Tea progress while removing OCI image entries.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/status"
)

const imageRemovalDefaultWidth = 96

type imageRemovalProgressMessage struct {
	progress oci.ImageRemovalProgress
}

type imageRemovalTick struct{}

type imageRemovalDone struct{}

type imageRemovalProgressRow struct {
	image oci.ImageInfo
	phase oci.ImageRemovalPhase
}

type imageRemovalProgressModel struct {
	ready   chan struct{}
	images  map[string]oci.ImageInfo
	rows    []imageRemovalProgressRow
	indexes map[string]int
	width   int
	frame   int
}

var _ tea.Model = imageRemovalProgressModel{}

func newImageRemovalProgressModel(
	ready chan struct{},
	images []oci.ImageInfo,
) imageRemovalProgressModel {
	byID := make(map[string]oci.ImageInfo, len(images))
	for _, image := range images {
		byID[image.ID] = image
	}
	return imageRemovalProgressModel{
		ready:   ready,
		images:  byID,
		indexes: make(map[string]int, len(images)),
	}
}

func (m imageRemovalProgressModel) Init() tea.Cmd {
	close(m.ready)
	return imageRemovalTickCommand()
}

func (m imageRemovalProgressModel) Update(
	message tea.Msg,
) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
	case imageRemovalProgressMessage:
		progress := message.progress
		index, found := m.indexes[progress.ID]
		if !found {
			switch progress.Phase {
			case oci.ImageRemovalPhaseRemoved,
				oci.ImageRemovalPhaseUntagged,
				oci.ImageRemovalPhaseFailed:
				break
			default:
				image := m.images[progress.ID]
				if image.ID == "" {
					image.ID = progress.ID
				}
				m.rows = append(m.rows, imageRemovalProgressRow{
					image: image,
					phase: progress.Phase,
				})
				m.indexes[progress.ID] = len(m.rows) - 1
			}
			break
		}
		switch progress.Phase {
		case oci.ImageRemovalPhaseRemoved,
			oci.ImageRemovalPhaseUntagged,
			oci.ImageRemovalPhaseFailed:
			m.rows = append(m.rows[:index], m.rows[index+1:]...)
			delete(m.indexes, progress.ID)
			for rowIndex := index; rowIndex < len(m.rows); rowIndex++ {
				m.indexes[m.rows[rowIndex].image.ID] = rowIndex
			}
		default:
			m.rows[index].phase = progress.Phase
		}
	case imageRemovalTick:
		m.frame++
		return m, imageRemovalTickCommand()
	case imageRemovalDone:
		return m, tea.Quit
	}

	return m, nil
}

func (m imageRemovalProgressModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = imageRemovalDefaultWidth
	}

	lines := make([]string, 0, len(m.rows))
	for _, row := range m.rows {
		lines = append(
			lines,
			renderImageRemovalProgressRow(row, m.frame, width),
		)
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func renderImageRemovalProgressRow(
	row imageRemovalProgressRow,
	frame int,
	width int,
) string {
	marker := status.SpinnerFrame(frame)
	action := "Removing"
	switch row.phase {
	case oci.ImageRemovalPhaseRemoved:
		marker = "✓"
		action = "Removed"
	case oci.ImageRemovalPhaseUntagged:
		marker = "✓"
		action = "Untagged"
	case oci.ImageRemovalPhaseFailed:
		marker = "✗"
		action = "Failed to remove"
	}

	line := fmt.Sprintf(
		"%s %s %s",
		marker,
		action,
		imageRemovalDescription(row.image),
	)
	return ansi.Truncate(line, width, "")
}

func imageRemovalDescription(image oci.ImageInfo) string {
	if image.Reference != "" {
		return fmt.Sprintf(
			"%s (%s)",
			tableCell(image.Reference),
			tableCell(imagePlatformString(image)),
		)
	}
	if image.Manifest.Digest != "" {
		return "image " + image.Manifest.Digest.String()
	}
	return "image " + tableCell(image.ShortID())
}

func imageRemovalTickCommand() tea.Cmd {
	return tea.Tick(status.SpinnerFrameInterval, func(time.Time) tea.Msg {
		return imageRemovalTick{}
	})
}

func removeSelectedImages(
	command *cobra.Command,
	store *oci.Store,
	images []oci.ImageInfo,
	skipConfirmation bool,
	forceRemoval bool,
	logger *diagnostic.Logger,
) error {
	if !skipConfirmation {
		confirmed, err := confirmImageRemoval(
			command.Context(),
			command.InOrStdin(),
			command.OutOrStdout(),
			images,
		)
		if err != nil {
			return err
		}
		if !confirmed {
			_, err := fmt.Fprintln(
				command.OutOrStdout(),
				"Image removal cancelled.",
			)
			return err
		}
	}

	if terminalStream(command.OutOrStdout()) {
		return runImageRemovalProgress(
			command.Context(),
			command.OutOrStdout(),
			store,
			images,
			forceRemoval,
			logger,
		)
	}

	return removeImagesPlain(
		command.Context(),
		command.OutOrStdout(),
		store,
		images,
		forceRemoval,
	)
}

func removeImagesPlain(
	ctx context.Context,
	output io.Writer,
	store *oci.Store,
	images []oci.ImageInfo,
	forceRemoval bool,
) error {
	removed, removeErr := store.RemoveImages(
		ctx,
		images,
		forceRemoval,
		nil,
	)
	var outputErr error
	for _, image := range removed {
		if _, err := fmt.Fprintln(
			output,
			image.ID,
		); err != nil {
			outputErr = errors.Join(outputErr, err)
		}
	}
	return errors.Join(removeErr, outputErr)
}

func runImageRemovalProgress(
	ctx context.Context,
	output io.Writer,
	store *oci.Store,
	images []oci.ImageInfo,
	force bool,
	logger *diagnostic.Logger,
) error {
	ready := make(chan struct{})
	model := newImageRemovalProgressModel(ready, images)
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
		logger.DebugError("start image removal progress", err)
		return removeImagesPlain(ctx, output, store, images, force)
	}

	imagesByID := make(map[string]oci.ImageInfo, len(images))
	for _, image := range images {
		imagesByID[image.ID] = image
	}
	width := terminalStreamWidth(output)
	if width <= 0 {
		width = imageRemovalDefaultWidth
	}
	_, removeErr := store.RemoveImages(
		ctx,
		images,
		force,
		func(progress oci.ImageRemovalProgress) {
			switch progress.Phase {
			case oci.ImageRemovalPhaseRemoved,
				oci.ImageRemovalPhaseUntagged,
				oci.ImageRemovalPhaseFailed:
				row := imageRemovalProgressRow{
					image: imagesByID[progress.ID],
					phase: progress.Phase,
				}
				printLine := tea.Println(
					renderImageRemovalProgressRow(row, 0, width),
				)
				program.Send(printLine())
			}
			program.Send(imageRemovalProgressMessage{
				progress: progress,
			})
		},
	)

	program.Send(imageRemovalDone{})
	programErr := <-programEnd
	logger.DebugError("render image removal progress", programErr)
	return removeErr
}
