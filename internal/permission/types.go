package permission

// Rule values and the policy outcome they resolve to.

import "fmt"

// Rule is the policy configured for an action, or its built-in default.
type Rule int

var _ fmt.Stringer = Rule(0)

const (
	// RuleUnset means no action-specific rule is configured.
	RuleUnset Rule = iota // nothing configured for the action
	// RuleAllow permits the action without prompting.
	RuleAllow // permit without asking
	// RuleDeny refuses the action without prompting.
	RuleDeny // refuse without asking
	// RuleAsk prompts unless yolo mode approves automatically.
	RuleAsk // prompt the user (yolo approves it)
	// RuleAlwaysAsk prompts even in yolo mode.
	RuleAlwaysAsk // prompt the user even under yolo
)

// ParseRule parses a configured rule value: "allow", "deny", "ask", or "always-ask".
func ParseRule(value string) (Rule, error) {
	switch value {
	case "allow":
		return RuleAllow, nil
	case "deny":
		return RuleDeny, nil
	case "ask":
		return RuleAsk, nil
	case "always-ask":
		return RuleAlwaysAsk, nil
	}
	return RuleUnset, fmt.Errorf("invalid permission rule %q (want allow, deny, ask, or always-ask)", value)
}

// String returns the canonical textual representation.
func (r Rule) String() string {
	switch r {
	case RuleAllow:
		return "allow"
	case RuleDeny:
		return "deny"
	case RuleAsk:
		return "ask"
	case RuleAlwaysAsk:
		return "always-ask"
	default:
		return "unset"
	}
}

// Outcome is the resolved policy outcome handed to a caller.
type Outcome int

var _ fmt.Stringer = Outcome(0)

const (
	// Deny refuses the requested action.
	Deny Outcome = iota // refuse the action
	// Allow permits the requested action.
	Allow // permit the action
	// Ask requires the user's decision before the action may run.
	Ask // ask the user
)

// String returns the canonical textual representation.
func (o Outcome) String() string {
	switch o {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}
