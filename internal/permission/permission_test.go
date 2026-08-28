package permission

// Tests permission-rule resolution.

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		def  Rule
		yolo bool
		want Decision
	}{
		// Explicit deny wins over everything, including yolo.
		{"explicit deny", RuleDeny, RuleAllow, false, Deny},
		{"explicit deny beats yolo", RuleDeny, RuleAllow, true, Deny},

		// Yolo approves everything that isn't an explicit deny — including explicit ask
		// and a default of deny.
		{"yolo approves unset", RuleUnset, RuleAsk, true, Allow},
		{"yolo approves explicit ask", RuleAsk, RuleAllow, true, Allow},
		{"yolo approves default-deny", RuleUnset, RuleDeny, true, Allow},

		// Explicit always-ask overrides yolo; a default of always-ask does not.
		{"always-ask overrides yolo", RuleAlwaysAsk, RuleAllow, true, Deny},
		{"always-ask denies without yolo", RuleAlwaysAsk, RuleAllow, false, Deny},
		{"default always-ask still yields to yolo", RuleUnset, RuleAlwaysAsk, true, Allow},

		// Explicit allow.
		{"explicit allow", RuleAllow, RuleAsk, false, Allow},

		// Explicit ask denies (overriding a permissive default).
		{"explicit ask denies", RuleAsk, RuleAllow, false, Deny},

		// Caller default when unset.
		{"default allow", RuleUnset, RuleAllow, false, Allow},
		{"default deny", RuleUnset, RuleDeny, false, Deny},
		{"default ask denies", RuleUnset, RuleAsk, false, Deny},
		{"unspecified default denies", RuleUnset, RuleUnset, false, Deny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.rule, tt.def, tt.yolo); got != tt.want {
				t.Fatalf("decision = %v, want %v", got, tt.want)
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
