package runtime

// Verifies the approval modal's key handling.

import "testing"

func TestDecisionForKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		selected    int
		wantSel     int
		wantDecided bool
		wantAllow   bool
	}{
		{"tab moves to deny", "\t", 0, 1, false, false},
		{"right wraps to approve", "\x1b[C", 1, 0, false, false},
		{"left wraps to deny", "\x1b[D", 0, 1, false, false},
		{"enter on approve allows", "\r", 0, 0, true, true},
		{"enter on deny denies", "\r", 1, 1, true, false},
		{"space confirms selection", " ", 0, 0, true, true},
		{"a approves", "a", 1, 0, true, true},
		{"d denies", "d", 0, 1, true, false},
		{"esc denies", "\x1b", 0, 1, true, false},
		{"unknown key ignored", "x", 0, 0, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, decided, allow := decisionForKey([]byte(tt.key), tt.selected)
			if sel != tt.wantSel || decided != tt.wantDecided || allow != tt.wantAllow {
				t.Errorf("decisionForKey(%q, %d) = (%d, %v, %v), want (%d, %v, %v)",
					tt.key, tt.selected, sel, decided, allow, tt.wantSel, tt.wantDecided, tt.wantAllow)
			}
		})
	}
}
