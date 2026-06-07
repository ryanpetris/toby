// Package permission resolves whether an action is allowed, denied, or must be asked,
// by applying the configured rule, yolo mode, and a caller-supplied default. There is
// no central table of actions: each service owns the default for its own actions and
// passes it in, so nothing here needs to know which actions exist.
package permission

// Resolve applies the policy precedence and returns the decision, or signals that the
// caller must prompt the user (mustAsk). When mustAsk is true the returned decision is
// meaningless. rule is the configured rule for the action (RuleUnset when nothing is
// configured); defaultRule is the caller's default, used only when nothing is
// configured. canAsk reports whether a prompt is possible at all (an interactive
// terminal with approvals enabled); when it is false an ask becomes a deny.
//
// Precedence:
//
//  1. an explicit deny rule always wins, even under yolo;
//  2. yolo approves everything else;
//  3. an explicit allow rule;
//  4. an explicit ask rule, otherwise the caller's default;
//  5. an ask outcome (and an unspecified default) becomes a prompt when canAsk,
//     otherwise a deny.
func Resolve(rule, defaultRule Rule, yolo, canAsk bool) (decision Decision, mustAsk bool) {
	switch {
	case rule == RuleDeny:
		return Deny, false
	case yolo:
		return Allow, false
	case rule == RuleAllow:
		return Allow, false
	}

	if rule == RuleUnset {
		rule = defaultRule
	}

	switch rule {
	case RuleAllow:
		return Allow, false
	case RuleDeny:
		return Deny, false
	default: // RuleAsk, or an unspecified default — prompt when possible
		if canAsk {
			return Deny, true
		}
		return Deny, false
	}
}
