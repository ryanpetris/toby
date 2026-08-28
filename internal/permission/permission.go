// Package permission resolves whether an action is allowed, denied, or must be asked,
// by applying the configured rule, yolo mode, and a caller-supplied default. There is
// no central table of actions: each service owns the default for its own actions and
// passes it in, so nothing here needs to know which actions exist.
package permission

// Resolve applies the policy precedence and returns the decision. rule is the
// configured rule for the action (RuleUnset when nothing is configured);
// defaultRule is the caller's default, used only when nothing is configured.
//
// Precedence:
//
//  1. an explicit deny rule always wins, even under yolo;
//  2. an explicit always-ask rule denies, even under yolo;
//  3. yolo approves everything else;
//  4. an explicit allow rule;
//  5. an explicit ask rule, otherwise the caller's default;
//  6. an ask outcome (and an unspecified default) becomes a deny.
//
// always-ask overrides yolo only as an explicit config rule; a caller default of
// always-ask does not, since yolo is the user's own override.
func Resolve(rule, defaultRule Rule, yolo bool) Decision {
	switch {
	case rule == RuleDeny:
		return Deny
	case rule == RuleAlwaysAsk:
		return Deny
	case yolo:
		return Allow
	case rule == RuleAllow:
		return Allow
	}

	if rule == RuleUnset {
		rule = defaultRule
	}

	switch rule {
	case RuleAllow:
		return Allow
	case RuleDeny:
		return Deny
	default: // RuleAsk, RuleAlwaysAsk as a default, or an unspecified default
		return Deny
	}
}
