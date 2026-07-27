package cli

// Renders OCI image tables and interactive removal confirmation.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"petris.dev/toby/internal/oci"
)

const (
	imageTableDefaultWidth          = 96
	imageTableMaximumReferenceWidth = 40
	imageTableMaximumPlatformWidth  = 20
)

func writeImageBubbleTable(
	output io.Writer,
	images []oci.ImageInfo,
) error {
	model := newImageTable(images, len(images)+1)
	_, err := fmt.Fprintln(output, model.View())
	return err
}

func newImageTable(
	images []oci.ImageInfo,
	height int,
) table.Model {
	rows := make([]table.Row, 0, len(images))
	for _, image := range images {
		rows = append(rows, table.Row{
			tableCell(image.ShortID()),
			tableCell(image.Reference),
			tableCell(imagePlatformString(image)),
			tableCell(image.ImageID()),
			imageStatus(image),
		})
	}

	columns := []table.Column{
		{Title: "ID", Width: tableColumnWidth(rows, 0, 12, 12)},
		{
			Title: "REFERENCE",
			Width: tableColumnWidth(
				rows,
				1,
				9,
				imageTableMaximumReferenceWidth,
			),
		},
		{
			Title: "PLATFORM",
			Width: tableColumnWidth(
				rows,
				2,
				8,
				imageTableMaximumPlatformWidth,
			),
		},
		{Title: "IMAGE ID", Width: tableColumnWidth(rows, 3, 12, 12)},
		{Title: "STATUS", Width: tableColumnWidth(rows, 4, 6, 8)},
	}
	styles := table.DefaultStyles()
	styles.Selected = lipgloss.NewStyle()

	return table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithHeight(max(height, 2)),
		table.WithWidth(imageTableDefaultWidth),
		table.WithStyles(styles),
	)
}

func confirmImageRemoval(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	images []oci.ImageInfo,
) (bool, error) {
	if !terminalStream(input) || !terminalStream(output) {
		return false, fmt.Errorf(
			"image removal confirmation requires a terminal; use --force to remove noninteractively",
		)
	}

	program := tea.NewProgram(
		newImageRemovalModel(images),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	result, err := program.Run()
	if err != nil {
		return false, fmt.Errorf("run image removal confirmation: %w", err)
	}
	model, ok := result.(imageRemovalModel)
	if !ok {
		return false, fmt.Errorf(
			"image removal confirmation returned %T",
			result,
		)
	}
	return model.confirmed, nil
}

type imageRemovalModel struct {
	table     table.Model
	count     int
	confirmed bool
	decided   bool
}

var _ tea.Model = imageRemovalModel{}

func newImageRemovalModel(images []oci.ImageInfo) imageRemovalModel {
	model := imageRemovalModel{
		table: newImageTable(images, min(len(images)+1, 12)),
		count: len(images),
	}
	model.table.Focus()
	return model
}

func (m imageRemovalModel) Init() tea.Cmd {
	return nil
}

func (m imageRemovalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.table.SetWidth(message.Width)
		m.table.SetHeight(max(min(m.count+1, message.Height-5), 2))
	case tea.KeyPressMsg:
		switch strings.ToLower(message.String()) {
		case "y":
			m.confirmed = true
			m.decided = true
			return m, tea.Quit
		case "n", "q", "esc", "ctrl+c":
			m.decided = true
			return m, tea.Quit
		}
	}

	var command tea.Cmd
	m.table, command = m.table.Update(message)
	return m, command
}

func (m imageRemovalModel) View() tea.View {
	if m.decided {
		return tea.NewView("")
	}

	noun := "images"
	if m.count == 1 {
		noun = "image"
	}
	body := fmt.Sprintf(
		"%s\n\nRemove %d %s? This cannot be undone.\n"+
			"y remove  n cancel  ↑/↓ scroll",
		m.table.View(),
		m.count,
		noun,
	)
	return tea.NewView(body)
}
