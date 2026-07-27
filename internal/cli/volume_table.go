package cli

// Renders persistent-volume tables and interactive removal confirmation.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"petris.dev/toby/internal/storage"
)

const (
	volumeTableDefaultWidth        = 80
	volumeTableMaximumNameWidth    = 20
	volumeTableMaximumProfileWidth = 12
	volumeTableMaximumPurposeWidth = 12
)

func writeVolumeBubbleTable(
	output io.Writer,
	volumes []storage.VolumeInfo,
) error {
	model := newVolumeTable(volumes, len(volumes)+1)
	_, err := fmt.Fprintln(output, model.View())
	return err
}

func newVolumeTable(
	volumes []storage.VolumeInfo,
	height int,
) table.Model {
	rows := make([]table.Row, 0, len(volumes))
	for _, volume := range volumes {
		status := "ready"
		if volume.Problem != "" {
			status = "invalid"
		}
		rows = append(rows, table.Row{
			volume.ShortID(),
			tableCell(string(volume.Type)),
			tableCell(volume.Name),
			tableCell(volume.Profile),
			tableCell(volume.Purpose),
			status,
		})
	}

	columns := []table.Column{
		{Title: "ID", Width: tableColumnWidth(rows, 0, 12, 12)},
		{Title: "TYPE", Width: tableColumnWidth(rows, 1, 4, 8)},
		{
			Title: "NAME",
			Width: tableColumnWidth(
				rows,
				2,
				4,
				volumeTableMaximumNameWidth,
			),
		},
		{
			Title: "PROFILE",
			Width: tableColumnWidth(
				rows,
				3,
				7,
				volumeTableMaximumProfileWidth,
			),
		},
		{
			Title: "PURPOSE",
			Width: tableColumnWidth(
				rows,
				4,
				7,
				volumeTableMaximumPurposeWidth,
			),
		},
		{Title: "STATUS", Width: tableColumnWidth(rows, 5, 6, 7)},
	}
	styles := table.DefaultStyles()
	styles.Selected = lipgloss.NewStyle()

	return table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithHeight(max(height, 2)),
		table.WithWidth(volumeTableDefaultWidth),
		table.WithStyles(styles),
	)
}

func confirmVolumeRemoval(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	volumes []storage.VolumeInfo,
) (bool, error) {
	if !terminalStream(input) || !terminalStream(output) {
		return false, fmt.Errorf(
			"volume removal confirmation requires a terminal; use --force to remove noninteractively",
		)
	}

	program := tea.NewProgram(
		newVolumeRemovalModel(volumes),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
	)
	result, err := program.Run()
	if err != nil {
		return false, fmt.Errorf("run volume removal confirmation: %w", err)
	}
	model, ok := result.(volumeRemovalModel)
	if !ok {
		return false, fmt.Errorf(
			"volume removal confirmation returned %T",
			result,
		)
	}
	return model.confirmed, nil
}

type volumeRemovalModel struct {
	table     table.Model
	count     int
	confirmed bool
	decided   bool
}

var _ tea.Model = volumeRemovalModel{}

func newVolumeRemovalModel(
	volumes []storage.VolumeInfo,
) volumeRemovalModel {
	model := volumeRemovalModel{
		table: newVolumeTable(volumes, min(len(volumes)+1, 12)),
		count: len(volumes),
	}
	model.table.Focus()
	return model
}

func (m volumeRemovalModel) Init() tea.Cmd {
	return nil
}

func (m volumeRemovalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
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

func (m volumeRemovalModel) View() tea.View {
	if m.decided {
		return tea.NewView("")
	}

	noun := "volumes"
	if m.count == 1 {
		noun = "volume"
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
