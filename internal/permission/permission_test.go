package permission

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		def     Rule
		yolo    bool
		canAsk  bool
		wantDec Decision
		wantAsk bool
	}{
		// Explicit deny wins over everything, including yolo.
		{"explicit deny", RuleDeny, RuleAllow, false, true, Deny, false},
		{"explicit deny beats yolo", RuleDeny, RuleAllow, true, true, Deny, false},

		// Yolo approves everything that isn't an explicit deny — including explicit ask
		// and a default of deny.
		{"yolo approves unset", RuleUnset, RuleAsk, true, true, Allow, false},
		{"yolo approves explicit ask", RuleAsk, RuleAllow, true, true, Allow, false},
		{"yolo approves default-deny", RuleUnset, RuleDeny, true, false, Allow, false},

		// Explicit allow.
		{"explicit allow", RuleAllow, RuleAsk, false, true, Allow, false},

		// Explicit ask forces a prompt (overriding a permissive default).
		{"explicit ask prompts", RuleAsk, RuleAllow, false, true, Deny, true},
		{"explicit ask without terminal denies", RuleAsk, RuleAllow, false, false, Deny, false},

		// Caller default when unset.
		{"default allow", RuleUnset, RuleAllow, false, true, Allow, false},
		{"default deny", RuleUnset, RuleDeny, false, true, Deny, false},
		{"default ask prompts", RuleUnset, RuleAsk, false, true, Deny, true},
		{"default ask without terminal denies", RuleUnset, RuleAsk, false, false, Deny, false},
		{"unspecified default prompts", RuleUnset, RuleUnset, false, true, Deny, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, ask := Resolve(tt.rule, tt.def, tt.yolo, tt.canAsk)
			if ask != tt.wantAsk {
				t.Fatalf("mustAsk = %v, want %v", ask, tt.wantAsk)
			}
			if !ask && dec != tt.wantDec {
				t.Fatalf("decision = %v, want %v", dec, tt.wantDec)
			}
		})
	}
}

func TestParseRule(t *testing.T) {
	for _, v := range []string{"allow", "deny", "ask"} {
		if _, err := ParseRule(v); err != nil {
			t.Errorf("ParseRule(%q) errored: %v", v, err)
		}
	}
	if _, err := ParseRule("maybe"); err == nil {
		t.Error("ParseRule(\"maybe\") should error")
	}
}
