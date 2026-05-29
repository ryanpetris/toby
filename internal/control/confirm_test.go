package control

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMountConfirmModelApprovesWithY(t *testing.T) {
	model, _ := newMountConfirmModel("/home/petris/Projects/chirp").Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	confirm := model.(mountConfirmModel)
	if !confirm.approved {
		t.Fatal("expected y to approve")
	}
}

func TestMountConfirmModelDeniesByDefaultOnEnter(t *testing.T) {
	model, _ := newMountConfirmModel("/home/petris/Projects/chirp").Update(tea.KeyMsg{Type: tea.KeyEnter})
	confirm := model.(mountConfirmModel)
	if confirm.approved {
		t.Fatal("expected default enter to deny")
	}
}

func TestMountConfirmModelViewIncludesPath(t *testing.T) {
	view := newMountConfirmModel("/home/petris/Projects/chirp").View()
	if !strings.Contains(view, "/home/petris/Projects/chirp") {
		t.Fatalf("view = %q, want path", view)
	}
}
