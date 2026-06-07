package permission

// Rule values and the binary decision they resolve to.

import "fmt"

// Rule is the policy configured for an action, or its built-in default.
type Rule int

const (
	RuleUnset     Rule = iota // nothing configured for the action
	RuleAllow                 // permit without asking
	RuleDeny                  // refuse without asking
	RuleAsk                   // prompt the user (yolo approves it)
	RuleAlwaysAsk             // prompt the user even under yolo
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

// Decision is the final, binary outcome handed to a caller.
type Decision int

const (
	Deny  Decision = iota // refuse the action
	Allow                 // permit the action
)

func (d Decision) String() string {
	if d == Allow {
		return "allow"
	}
	return "deny"
}
