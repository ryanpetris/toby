package control

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func RunMountConfirmation(ctx context.Context, path string) (bool, error) {
	program := tea.NewProgram(newMountConfirmModel(path), tea.WithContext(ctx))
	final, err := program.Run()
	if err != nil {
		return false, err
	}
	model, ok := final.(mountConfirmModel)
	if !ok {
		return false, nil
	}
	return model.approved, nil
}

type mountConfirmModel struct {
	path     string
	choice   int
	approved bool
}

func newMountConfirmModel(path string) mountConfirmModel {
	return mountConfirmModel{path: path}
}

func (m mountConfirmModel) Init() tea.Cmd { return nil }

func (m mountConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q", "n":
			m.approved = false
			return m, tea.Quit
		case "y":
			m.approved = true
			return m, tea.Quit
		case "left", "right", "h", "l", "tab", "shift+tab":
			m.choice = 1 - m.choice
		case "enter":
			m.approved = m.choice == 1
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m mountConfirmModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render("Toby mount request")
	question := "Allow this project directory to be mounted into the current sandbox?"
	path := lipgloss.NewStyle().Bold(true).Render(m.path)
	deny := button("Deny", m.choice == 0)
	approve := button("Approve", m.choice == 1)
	help := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Use left/right then enter. Press y to approve, n or esc to deny.")
	return strings.Join([]string{title, "", question, "", path, "", deny + "  " + approve, "", help}, "\n")
}

func button(label string, selected bool) string {
	style := lipgloss.NewStyle().Padding(0, 1)
	if selected {
		style = style.Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("10"))
	}
	return style.Render(label)
}
